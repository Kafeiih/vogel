package process

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"

	domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/process"
)

const CaseDomain = "bpms"

type Service struct {
	db       DB
	engine   *workflow.Engine
	cases    CaseReader
	values   ValueRepository
	catalog  *Catalog
	profiles domain.ProfileResolver
}

type ServiceDeps struct {
	DB       DB
	Engine   *workflow.Engine
	Cases    CaseReader
	Values   ValueRepository
	Catalog  *Catalog
	Profiles domain.ProfileResolver
}

func NewService(d ServiceDeps) *Service {
	return &Service{db: d.DB, engine: d.Engine, cases: d.Cases, values: d.Values, catalog: d.Catalog, profiles: d.Profiles}
}

func (s *Service) Start(ctx context.Context, actor, process, action string, values map[string]any) (*workflow.Case, error) {
	p, ok := s.catalog.Latest(process)
	if !ok {
		return nil, domain.ErrProcessNotFound
	}
	start, _ := p.Definition.StartNode()
	clean, err := p.ValidateNode(start.ID, values, nil)
	if err != nil {
		return nil, err
	}

	var c *workflow.Case
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		opened, err := s.engine.Open(ctx, tx, workflow.OpenInput{
			Definition: p.Definition.Name,
			Version:    p.Definition.Version,
			Domain:     CaseDomain,
			ExternalID: uuid.NewString(),
			ActorID:    actor,
		})
		if err != nil {
			return err
		}
		if err := s.values.Save(ctx, tx, opened.ID, start.ID, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: opened.ID, Action: action, ActorID: actor})
		return err
	})
	return c, err
}

func (s *Service) Inbox(ctx context.Context, actor string) ([]workflow.Case, error) {
	states := s.profiledStates()
	if len(states) == 0 {
		return nil, nil
	}

	unclaimed, err := s.engine.Inbox(ctx, s.db, workflow.InboxFilter{Domain: CaseDomain, States: states, Unassigned: true})
	if err != nil {
		return nil, err
	}
	out := make([]workflow.Case, 0, len(unclaimed))
	for i := range unclaimed {
		ok, err := s.holdsCurrent(ctx, s.db, unclaimed[i].ID, actor)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, unclaimed[i])
		}
	}

	claimed, err := s.engine.Inbox(ctx, s.db, workflow.InboxFilter{Domain: CaseDomain, States: states, AssignedTo: actor})
	if err != nil {
		return nil, err
	}
	return append(out, claimed...), nil
}

func (s *Service) profiledStates() []string {
	seen := map[string]bool{}
	var states []string
	for _, p := range s.catalog.All() {
		for _, id := range p.ProfiledNodes() {
			if !seen[id] {
				seen[id] = true
				states = append(states, id)
			}
		}
	}
	return states
}

func (s *Service) Claim(ctx context.Context, actor string, caseID uuid.UUID) (*workflow.Case, error) {
	var c *workflow.Case
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		ok, err := s.holdsCurrent(ctx, tx, caseID, actor)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrNotHolder
		}
		c, err = s.engine.Claim(ctx, tx, caseID, actor)
		return err
	})
	return c, err
}

func (s *Service) Submit(ctx context.Context, actor string, caseID uuid.UUID, action string, values map[string]any) (*workflow.Case, error) {
	var c *workflow.Case
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		current, err := s.cases.GetByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if current.Status == workflow.StatusClosed {
			return workflow.ErrCaseClosed
		}
		// Engine.Move does not compare its actor with the assignee.
		if current.AssignedTo != actor {
			return domain.ErrNotClaimant
		}
		p, ok := s.catalog.Get(current.Definition, current.Version)
		if !ok {
			return fmt.Errorf("%w: %s@%d", domain.ErrProcessNotFound, current.Definition, current.Version)
		}
		caseData, err := s.values.Current(ctx, tx, caseID)
		if err != nil {
			return err
		}
		clean, err := p.ValidateNode(current.State, values, caseData)
		if err != nil {
			return err
		}
		if err := s.values.Save(ctx, tx, caseID, current.State, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: caseID, Action: action, ActorID: actor})
		return err
	})
	return c, err
}

type CaseView struct {
	Case    *workflow.Case
	Values  map[string]any
	History []domain.CaseValue
}

func (s *Service) View(ctx context.Context, actor string, caseID uuid.UUID) (*CaseView, error) {
	c, err := s.cases.GetByID(ctx, s.db, caseID)
	if err != nil {
		return nil, err
	}
	allowed, err := s.related(ctx, actor, c)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, domain.ErrNoAccess
	}

	values, err := s.values.Current(ctx, s.db, caseID)
	if err != nil {
		return nil, err
	}
	history, err := s.values.History(ctx, s.db, caseID)
	if err != nil {
		return nil, err
	}
	return &CaseView{Case: c, Values: values, History: history}, nil
}

func (s *Service) related(ctx context.Context, actor string, c *workflow.Case) (bool, error) {
	if c.AssignedTo == actor {
		return true, nil
	}
	events, err := s.engine.History(ctx, s.db, c.ID)
	if err != nil {
		return false, err
	}
	// Move clears the assignment, so a past handler is only in the events.
	for i := range events {
		if events[i].ActorID == actor {
			return true, nil
		}
	}
	if c.Status == workflow.StatusClosed {
		return false, nil
	}
	return s.holdsCurrent(ctx, s.db, c.ID, actor)
}

func (s *Service) holdsCurrent(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID, actor string) (bool, error) {
	e, err := s.engine.EligibilityFor(ctx, db, caseID)
	if err != nil {
		return false, err
	}
	return domain.Holds(ctx, s.profiles, e, actor)
}
