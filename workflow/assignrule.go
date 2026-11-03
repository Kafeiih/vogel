package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/kafeiih/vogel/pgxtx"
)

// ErrUnknownAssignRule reports a Definition declaring a Node.AssignRule while
// the Engine has no Config.Assignees.
var ErrUnknownAssignRule = errors.New("workflow: assign rule not known to the assignee resolver")

// AssignInput is what an AssigneeResolver receives: the rule a node declares,
// the case and token as they are being written, the node being entered, and the
// case's whole history, including the events this same call has planned so far.
// Case.State and Case.AssignedTo still hold their values from before the call,
// because they are projected from the tokens when the call persists: read Node
// and Token for where the token is going.
type AssignInput struct {
	Rule    string
	Case    Case
	Token   Token
	Node    Node
	History []Event
}

// AssigneeResolver turns a Node.AssignRule into the person a token entering that
// node goes to (see Config.Assignees). vogel never interprets the rule. An empty
// answer leaves the token in the node's pool. A returned error fails the call
// that entered the node, with nothing persisted.
//
// Like a GuardFunc it runs inside the caller's open transaction: it must not
// call the network, so a consumer answers from local data.
type AssigneeResolver interface {
	Assignee(ctx context.Context, db pgxtx.DBTX, in AssignInput) (string, error)
}

// LastActorOf returns the actor of the latest EventMoved leaving node with a
// non-system actor, or empty. It searches the whole history, not one scope,
// because each round of a parallel block opens a new scope. Rules that keep the
// person of an earlier step build on it (see AssigneeResolver).
func LastActorOf(history []Event, node string) string {
	var actor string
	var latest int64
	for i := range history {
		ev := &history[i]
		if ev.Kind != EventMoved || ev.FromState != node || ev.Actor.Kind == ActorSystem || ev.Actor.ID == "" {
			continue
		}
		if actor == "" || ev.Seq > latest {
			actor, latest = ev.Actor.ID, ev.Seq
		}
	}
	return actor
}

// AssignRules returns the sorted, deduplicated set of assign rule names the
// Engine may resolve for this definition.
func (d Definition) AssignRules() []string {
	seen := make(map[string]bool)
	var names []string
	for i := range d.Nodes {
		n := &d.Nodes[i]
		if n.AssignRule == "" || seen[n.AssignRule] {
			continue
		}
		seen[n.AssignRule] = true
		names = append(names, n.AssignRule)
	}
	sort.Strings(names)
	return names
}

// applyAssignRule asks Config.Assignees who tok goes to on resting at node n,
// assigning it and appending an EventAssigned when the answer names a person.
// A node without a rule costs nothing: it reads no history.
func (e *Engine) applyAssignRule(ctx context.Context, db pgxtx.DBTX, run *caseRun, tok *Token, n Node) error {
	if n.AssignRule == "" {
		return nil
	}
	if e.assignees == nil {
		return fmt.Errorf("node %q: %w: %s", n.ID, ErrUnknownAssignRule, n.AssignRule)
	}
	history, err := run.fullHistory(ctx, db, e)
	if err != nil {
		return fmt.Errorf("node %q: assign rule %q: %w", n.ID, n.AssignRule, err)
	}
	to, err := e.assignees.Assignee(ctx, db, AssignInput{Rule: n.AssignRule, Case: *run.c, Token: *tok, Node: n, History: history})
	if err != nil {
		return fmt.Errorf("node %q: assign rule %q: %w", n.ID, n.AssignRule, err)
	}
	if to == "" {
		return nil
	}
	detail, err := marshalDetail(AssignmentDetail{To: to, Reason: "assign_rule:" + n.AssignRule})
	if err != nil {
		return err
	}
	tok.AssignedTo = to
	run.emit(&Event{
		Kind: EventAssigned, TokenID: tok.ID, ScopeID: tok.ScopeID,
		FromState: n.ID, ToState: n.ID, Detail: detail,
	})
	return nil
}

// fullHistory returns the stored history, read at most once per call, followed
// by the events the call has planned so far, shaped as an appended event reads.
func (r *caseRun) fullHistory(ctx context.Context, db pgxtx.DBTX, e *Engine) ([]Event, error) {
	if !r.historyRead {
		stored, err := e.listEvents(ctx, db, r.c.ID)
		if err != nil {
			return nil, err
		}
		r.history, r.historyRead = stored, true
	}
	out := make([]Event, 0, len(r.history)+len(r.events))
	out = append(out, r.history...)
	for i := range r.events {
		ev := r.events[i]
		ev.CaseID, ev.Tenant, ev.ActorID, ev.SchemaVersion = r.c.ID, r.c.Tenant, ev.Actor.ID, CurrentEventSchema
		if len(ev.Detail) == 0 {
			ev.Detail = emptyDetail()
		}
		out = append(out, ev)
	}
	return out, nil
}
