package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kafeiih/vogel/request"
	"github.com/kafeiih/vogel/workflow"
	wfpg "github.com/kafeiih/vogel/workflow/postgres"
)

// caseDomain is the workflow_case.domain of every case this example opens.
const caseDomain = "bpms"

var (
	errProcessNotFound = errors.New("process not found")
	// errNotHolder: the actor does not hold the profile of the case's
	// current node.
	errNotHolder = errors.New("actor does not hold the profile for this task")
	// errNotClaimant: only the person who claimed the task may submit it.
	errNotClaimant = errors.New("task is not claimed by this actor")
	// errNoAccess: the actor is not related to the case.
	errNoAccess = errors.New("actor has no access to this case")
)

// FormError carries one message per offending field of a submission.
type FormError struct {
	Fields request.FieldErrors
}

func (e *FormError) Error() string { return fmt.Sprintf("invalid form: %d field(s)", len(e.Fields)) }

// Service runs cases of published processes. The engine moves cases and
// knows nothing of forms; Service validates each node's form against the
// version the case was opened with, stores the values, and only then moves
// the case, all in one transaction.
type Service struct {
	pool     *pgxpool.Pool
	engine   *workflow.Engine
	repo     *wfpg.Repository
	catalog  *Catalog
	profiles ProfileResolver
}

// NewService wires a Service. repo must be the repository engine was built
// with: Service reads cases through it because the engine has no getter.
func NewService(pool *pgxpool.Pool, engine *workflow.Engine, repo *wfpg.Repository, catalog *Catalog, profiles ProfileResolver) *Service {
	return &Service{pool: pool, engine: engine, repo: repo, catalog: catalog, profiles: profiles}
}

// Start opens a case on the latest version of process, stores the start
// node's form and takes action out of the start node. Whoever opens the
// case fills in the start node, so it needs no claim.
func (s *Service) Start(ctx context.Context, actor, process, action string, values map[string]any) (*workflow.Case, error) {
	p, ok := s.catalog.Latest(process)
	if !ok {
		return nil, errProcessNotFound
	}
	start, _ := p.Definition.StartNode()
	clean, err := validateNodeForm(p, start.ID, values, nil)
	if err != nil {
		return nil, err
	}

	var c *workflow.Case
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		opened, err := s.engine.Open(ctx, tx, workflow.OpenInput{
			Definition: p.Definition.Name,
			Version:    p.Definition.Version,
			Domain:     caseDomain,
			ExternalID: uuid.NewString(),
			ActorID:    actor,
		})
		if err != nil {
			return err
		}
		if err := SaveCaseValues(ctx, tx, opened.ID, start.ID, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: opened.ID, Action: action, ActorID: actor})
		return err
	})
	return c, err
}

// Inbox lists the open cases actor can work on: the unclaimed ones whose
// current node actor's profile holds right now, plus the ones actor has
// claimed. The profile check runs at read time, so a subrogation shows up
// at once and nothing is reassigned.
func (s *Service) Inbox(ctx context.Context, actor string) ([]workflow.Case, error) {
	states := s.profiledStates()
	if len(states) == 0 {
		return nil, nil
	}

	unclaimed, err := s.engine.Inbox(ctx, s.pool, workflow.InboxFilter{Domain: caseDomain, States: states, Unassigned: true})
	if err != nil {
		return nil, err
	}
	out := make([]workflow.Case, 0, len(unclaimed))
	for i := range unclaimed {
		c := &unclaimed[i]
		e, err := s.engine.EligibilityFor(ctx, s.pool, c.ID)
		if err != nil {
			return nil, err
		}
		ok, err := holds(ctx, s.profiles, e, actor)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *c)
		}
	}

	claimed, err := s.engine.Inbox(ctx, s.pool, workflow.InboxFilter{Domain: caseDomain, States: states, AssignedTo: actor})
	if err != nil {
		return nil, err
	}
	return append(out, claimed...), nil
}

// profiledStates is every node ID, across published versions, that names
// a profile: the only states an inbox can hold.
func (s *Service) profiledStates() []string {
	seen := map[string]bool{}
	var states []string
	for _, p := range s.catalog.All() {
		for _, n := range p.Definition.Nodes {
			if !n.Eligible.IsZero() && !seen[n.ID] {
				seen[n.ID] = true
				states = append(states, n.ID)
			}
		}
	}
	return states
}

// Claim fixes actor as the person working the case, if actor holds the
// profile of its current node right now.
func (s *Service) Claim(ctx context.Context, actor string, caseID uuid.UUID) (*workflow.Case, error) {
	var c *workflow.Case
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := s.engine.EligibilityFor(ctx, tx, caseID)
		if err != nil {
			return err
		}
		ok, err := holds(ctx, s.profiles, e, actor)
		if err != nil {
			return err
		}
		if !ok {
			return errNotHolder
		}
		c, err = s.engine.Claim(ctx, tx, caseID, actor)
		return err
	})
	return c, err
}

// Submit stores the current node's form and takes action. Only the
// claimant may submit: the engine's Move does not compare its actor with
// the case's assignee, so this check is the caller's job.
func (s *Service) Submit(ctx context.Context, actor string, caseID uuid.UUID, action string, values map[string]any) (*workflow.Case, error) {
	var c *workflow.Case
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := s.repo.GetByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if current.Status == workflow.StatusClosed {
			return workflow.ErrCaseClosed
		}
		if current.AssignedTo != actor {
			return errNotClaimant
		}
		p, ok := s.catalog.Get(current.Definition, current.Version)
		if !ok {
			return fmt.Errorf("%w: %s@%d", errProcessNotFound, current.Definition, current.Version)
		}
		caseData, err := CurrentCaseValues(ctx, tx, caseID)
		if err != nil {
			return err
		}
		clean, err := validateNodeForm(p, current.State, values, caseData)
		if err != nil {
			return err
		}
		if err := SaveCaseValues(ctx, tx, caseID, current.State, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: caseID, Action: action, ActorID: actor})
		return err
	})
	return c, err
}

// CaseView is a case with its data, for whoever is related to it.
type CaseView struct {
	Case    *workflow.Case
	Values  map[string]any
	History []CaseValue
}

// View returns the case if actor opened it, is working it, or holds the
// profile of its current node.
func (s *Service) View(ctx context.Context, actor string, caseID uuid.UUID) (*CaseView, error) {
	c, err := s.repo.GetByID(ctx, s.pool, caseID)
	if err != nil {
		return nil, err
	}
	allowed, err := s.related(ctx, actor, c)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errNoAccess
	}

	values, err := CurrentCaseValues(ctx, s.pool, caseID)
	if err != nil {
		return nil, err
	}
	history, err := CaseValueHistory(ctx, s.pool, caseID)
	if err != nil {
		return nil, err
	}
	return &CaseView{Case: c, Values: values, History: history}, nil
}

func (s *Service) related(ctx context.Context, actor string, c *workflow.Case) (bool, error) {
	if c.AssignedTo == actor {
		return true, nil
	}
	events, err := s.engine.History(ctx, s.pool, c.ID)
	if err != nil {
		return false, err
	}
	if len(events) > 0 && events[0].ActorID == actor {
		return true, nil
	}
	if c.Status == workflow.StatusClosed {
		return false, nil
	}
	e, err := s.engine.EligibilityFor(ctx, s.pool, c.ID)
	if err != nil {
		return false, err
	}
	return holds(ctx, s.profiles, e, actor)
}

// validateNodeForm validates values against nodeID's form. A node without a
// form accepts only an empty submission.
func validateNodeForm(p *Process, nodeID string, values, caseData map[string]any) (map[string]any, error) {
	form, ok := p.Forms[nodeID]
	if !ok {
		if len(values) > 0 {
			errs := request.FieldErrors{}
			for k := range values {
				errs[k] = "is not a field of this form"
			}
			return nil, &FormError{Fields: errs}
		}
		return nil, nil
	}
	clean, errs := form.Validate(values, caseData)
	if len(errs) > 0 {
		return nil, &FormError{Fields: errs}
	}
	return clean, nil
}
