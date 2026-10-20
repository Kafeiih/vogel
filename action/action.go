// Package action runs the declarative effects a workflow.Definition names
// (workflow.Node.Effects and workflow.Transition.Effects) as background jobs,
// never inside the request that advanced the case.
//
// Registry is the workflow.EffectSink: Engine.Register asks it whether every
// effect name is known (HasEffect), and Engine.Open / Engine.Move hand it the
// events they appended (Dispatch). Dispatch turns each event into effect
// names — the effects of the node the event entered, then those of the
// transition or decision route it took — and enqueues one River job per
// (case id, event seq, effect name) with worker.Queue.EnqueueTx, on the same
// transaction the engine wrote the case and its events in. A rollback of
// that transaction therefore enqueues nothing, and a commit makes the jobs
// visible together with the state change (transactional outbox).
//
// Worker is the River worker for those jobs. It looks the effect name up in
// the Registry and calls its Handler with a Run describing the case and the
// event. A Handler error is retried by River with its usual backoff; return
// river.JobCancel(err) from a Handler for a failure that can never succeed,
// so River stops retrying. Jobs are unique on (case id, seq, effect), so a
// re-dispatch of the same event while its job is still retained by River is
// a no-op. Execution is at-least-once: a Handler must tolerate running again
// after a crash or a retry.
//
// Anything a later workflow step depends on must be checked by a guard, never
// assumed to have run: an effect runs asynchronously and may still be pending
// or failing when the next human action arrives.
//
// Built-in handlers: NotifyHandler adapts a notification.Notifier. A webhook
// handler is planned but not part of this package yet.
//
// Wiring:
//
//	reg := action.NewRegistry(queue)
//	_ = reg.Register("notify", action.NotifyHandler(notifier, buildMessages))
//	engine := workflow.New(repo, workflow.Config{}, logger, workflow.WithEffectSink(reg))
//
//	// worker binary
//	workers := river.NewWorkers()
//	_ = action.AddWorker(workers, action.NewWorker(reg))
package action

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
)

// Kind is the River job kind of every effect job.
const Kind = "vogel_action"

// Sentinel errors callers can branch on via errors.Is. An unknown effect name
// is reported with workflow.ErrUnknownEffect.
var (
	// ErrInvalidRegistration reports a Register call with an empty name or
	// a nil Handler.
	ErrInvalidRegistration = errors.New("action: effect name and handler are required")
	// ErrDuplicateEffect reports a Register call for a name already
	// registered.
	ErrDuplicateEffect = errors.New("action: effect already registered")
)

// Run describes one effect execution: which effect, for which case, caused by
// which event. It carries only the case reference (Domain, ExternalID), never
// domain data; a Handler that needs more looks it up in its own store.
type Run struct {
	Effect     string
	CaseID     uuid.UUID
	Seq        int64
	Definition string
	Version    int
	Domain     string
	ExternalID string
	Unit       string
	// ActorID is the actor of the event that caused this effect.
	ActorID string
	// Attempt is River's attempt number, starting at 1.
	Attempt int
}

// Handler runs one effect. Returning an error makes River retry the job;
// returning river.JobCancel(err) fails it permanently. A Handler must be
// idempotent: it may run more than once for the same Run.
type Handler interface {
	Handle(ctx context.Context, r Run) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, r Run) error

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, r Run) error { return f(ctx, r) }

// IsCancel reports whether err asks River to stop retrying (river.JobCancel).
func IsCancel(err error) bool {
	var cancel *rivertype.JobCancelError
	return errors.As(err, &cancel)
}

// Args is the River job payload for one effect. CaseID, Seq and Effect are
// the uniqueness key; the rest is the case reference copied at dispatch time.
type Args struct {
	CaseID     uuid.UUID `json:"case_id" river:"unique"`
	Seq        int64     `json:"seq" river:"unique"`
	Effect     string    `json:"effect" river:"unique"`
	Definition string    `json:"definition"`
	Version    int       `json:"version"`
	Domain     string    `json:"domain"`
	ExternalID string    `json:"external_id"`
	Unit       string    `json:"unit"`
	ActorID    string    `json:"actor_id"`
}

// Kind returns the stable River job kind, Kind.
func (Args) Kind() string { return Kind }

// insertOpts makes a job unique on its river:"unique" fields (case id, seq,
// effect) within River's default unique states, so a second insert of the
// same effect for the same event is skipped as a duplicate.
func insertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// Registry maps effect names to Handlers and implements workflow.EffectSink.
// It is safe for concurrent use.
type Registry struct {
	queue worker.Queue

	mu       sync.RWMutex
	handlers map[string]Handler
}

var _ workflow.EffectSink = (*Registry)(nil)

// NewRegistry creates an empty Registry that enqueues effect jobs on q. The
// API process passes an insert-only queue; the worker binary may share it.
func NewRegistry(q worker.Queue) *Registry {
	return &Registry{queue: q, handlers: make(map[string]Handler)}
}

// Register makes h the Handler for the effect name. It fails with
// ErrInvalidRegistration for an empty name or nil h, and ErrDuplicateEffect
// when name is already registered. Register every effect before registering
// the Definitions that name them on the workflow.Engine.
func (r *Registry) Register(name string, h Handler) error {
	if name == "" || h == nil {
		return ErrInvalidRegistration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.handlers[name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateEffect, name)
	}
	r.handlers[name] = h
	return nil
}

// HasEffect reports whether name is registered.
func (r *Registry) HasEffect(name string) bool {
	_, ok := r.handler(name)
	return ok
}

func (r *Registry) handler(name string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[name]
	return h, ok
}

// Dispatch enqueues one job per effect the events trigger, in event order
// and, within one event, destination node effects before the taken
// transition's (or decision route's) effects. EventOpened and EventMoved
// trigger Node.Effects of ToState; EventMoved also triggers the
// Transition.Effects of the transition identified by (FromState, Action).
// Every other event kind triggers nothing.
//
// Jobs are enqueued on the caller's transaction, which must be db itself: the
// engine wrote the case and its events through db, so a transaction found
// only on ctx would let the jobs commit or roll back apart from them. With
// effects to enqueue and a db that is not a pgx.Tx, Dispatch fails with an
// error wrapping pgxtx.ErrNoTx and enqueues nothing. An effect name not registered fails
// with workflow.ErrUnknownEffect before anything is enqueued.
func (r *Registry) Dispatch(ctx context.Context, db pgxtx.DBTX, d workflow.Definition, c workflow.Case, events []workflow.Event) error {
	jobs, err := r.plan(d, c, events)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}
	tx, err := resolveTx(db)
	if err != nil {
		return fmt.Errorf("action: dispatch case %s: %w", c.ID, err)
	}
	for _, args := range jobs {
		if _, err := r.queue.EnqueueTx(ctx, tx, args, insertOpts()); err != nil {
			return fmt.Errorf("action: dispatch case %s: enqueue %q (seq %d): %w", c.ID, args.Effect, args.Seq, err)
		}
	}
	return nil
}

// plan maps events to the job args Dispatch enqueues, in order.
func (r *Registry) plan(d workflow.Definition, c workflow.Case, events []workflow.Event) ([]Args, error) {
	var jobs []Args
	for _, evt := range events {
		for _, name := range effectsFor(d, evt) {
			if !r.HasEffect(name) {
				return nil, fmt.Errorf("action: dispatch case %s: %w: %s", c.ID, workflow.ErrUnknownEffect, name)
			}
			jobs = append(jobs, Args{
				CaseID:     c.ID,
				Seq:        evt.Seq,
				Effect:     name,
				Definition: c.Definition,
				Version:    c.Version,
				Domain:     c.Domain,
				ExternalID: c.ExternalID,
				Unit:       c.Unit,
				ActorID:    evt.ActorID,
			})
		}
	}
	return jobs, nil
}

// effectsFor returns the effect names one event triggers.
func effectsFor(d workflow.Definition, evt workflow.Event) []string {
	var names []string
	switch evt.Kind {
	case workflow.EventOpened, workflow.EventMoved:
		if n, ok := d.Node(evt.ToState); ok {
			names = append(names, n.Effects...)
		}
	default:
		return nil
	}
	if evt.Kind == workflow.EventMoved {
		for _, tr := range d.TransitionsFrom(evt.FromState) {
			if tr.Action == evt.Action {
				names = append(names, tr.Effects...)
				break
			}
		}
	}
	return names
}

// resolveTx returns db when it is a transaction, else an error wrapping pgxtx.ErrNoTx.
func resolveTx(db pgxtx.DBTX) (pgx.Tx, error) {
	if tx, ok := db.(pgx.Tx); ok && tx != nil {
		return tx, nil
	}
	return nil, fmt.Errorf("action: dispatch needs db to be the caller's transaction: %w", pgxtx.ErrNoTx)
}
