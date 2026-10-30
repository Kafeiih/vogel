package process

import (
	"context"
	"errors"
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

func (s *Service) Publish(ctx context.Context, spec domain.ProcessSpec) (*domain.Process, error) {
	return s.catalog.Publish(ctx, s.db, spec)
}

func (s *Service) Latest(name string) (*domain.Process, bool) {
	return s.catalog.Latest(name)
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
			Actor:      workflow.Actor{Kind: workflow.ActorHuman, ID: actor},
		})
		if err != nil {
			return err
		}
		if err := s.values.Save(ctx, tx, opened.ID, start.ID, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: opened.ID, Action: action, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: actor}})
		return err
	})
	return c, err
}

// Inbox returns the work items the actor can act on: the unclaimed tokens
// whose (position, unit) the actor holds, then the tokens claimed by them.
// The holder check runs once per distinct eligibility the catalog declares,
// and the database filters the tokens by it, so the page is never cut short
// by a filter applied after it and no case costs a resolver call of its own.
func (s *Service) Inbox(ctx context.Context, actor string) ([]workflow.WorkItem, error) {
	states := s.profiledStates()
	if len(states) == 0 {
		return nil, nil
	}
	held, err := s.heldEligibilities(ctx, actor)
	if err != nil {
		return nil, err
	}

	var out []workflow.WorkItem
	if len(held) > 0 {
		unclaimed, err := s.engine.Inbox(ctx, s.db, workflow.InboxFilter{Domain: CaseDomain, Eligibility: held, Unassigned: true})
		if err != nil {
			return nil, err
		}
		out = append(out, unclaimed...)
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

// heldEligibilities returns the distinct eligibilities declared by the
// catalog's nodes that actor holds. Cases are opened without a unit, so a
// node's eligibility resolves to its own unit, the value its tokens store.
func (s *Service) heldEligibilities(ctx context.Context, actor string) ([]workflow.Eligibility, error) {
	seen := map[workflow.Eligibility]bool{}
	var held []workflow.Eligibility
	for _, p := range s.catalog.All() {
		for i := range p.Definition.Nodes {
			n := &p.Definition.Nodes[i]
			if n.Eligible.IsZero() {
				continue
			}
			e := workflow.Eligibility{Position: n.Eligible.Position, Unit: n.Eligible.ResolveUnit("")}
			if seen[e] {
				continue
			}
			seen[e] = true
			ok, err := domain.Holds(ctx, s.profiles, e, actor)
			if err != nil {
				return nil, err
			}
			if ok {
				held = append(held, e)
			}
		}
	}
	return held, nil
}

// Claim assigns a token of the case to the actor, who must hold its
// eligibility. A zero tokenID names the case's only active token.
func (s *Service) Claim(ctx context.Context, actor string, caseID, tokenID uuid.UUID) (*workflow.Case, error) {
	var c *workflow.Case
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tok, err := s.activeToken(ctx, tx, caseID, tokenID)
		if err != nil {
			return err
		}
		ok, err := domain.Holds(ctx, s.profiles, tok.Eligible, actor)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrNotHolder
		}
		c, err = s.engine.ClaimWith(ctx, tx, workflow.ClaimInput{CaseID: caseID, TokenID: tok.ID, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: actor}})
		return err
	})
	return c, err
}

// activeToken returns the active token tokenID names, or the case's only
// active token when tokenID is zero (workflow.ErrTokenRequired with several).
func (s *Service) activeToken(ctx context.Context, db pgxtx.DBTX, caseID, tokenID uuid.UUID) (workflow.Token, error) {
	tokens, err := s.engine.Tokens(ctx, db, caseID)
	if err != nil {
		return workflow.Token{}, err
	}
	var active []workflow.Token
	for i := range tokens {
		if tokens[i].Status == workflow.TokenActive {
			active = append(active, tokens[i])
		}
	}
	if tokenID == uuid.Nil {
		switch len(active) {
		case 1:
			return active[0], nil
		case 0:
			return workflow.Token{}, workflow.ErrCaseClosed
		default:
			return workflow.Token{}, workflow.ErrTokenRequired
		}
	}
	for i := range active {
		if active[i].ID == tokenID {
			return active[i], nil
		}
	}
	return workflow.Token{}, workflow.ErrTokenNotFound
}

// Submit moves a token of the case with its form values. A zero tokenID
// names the case's only active token.
func (s *Service) Submit(ctx context.Context, actor string, caseID, tokenID uuid.UUID, action string, values map[string]any) (*workflow.Case, error) {
	var c *workflow.Case
	// moved is the token Move was asked to move, for the failure record.
	moved := tokenID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		current, err := s.cases.GetByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if current.Status != workflow.StatusOpen {
			return workflow.ErrCaseClosed
		}
		tok, err := s.activeToken(ctx, tx, caseID, tokenID)
		if err != nil {
			return err
		}
		moved = tok.ID
		// Engine.Move does not compare its actor with the assignee.
		if tok.AssignedTo != actor {
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
		clean, err := p.ValidateNode(tok.Node, values, caseData)
		if err != nil {
			return err
		}
		if err := s.values.Save(ctx, tx, caseID, tok.Node, actor, clean); err != nil {
			return err
		}
		c, err = s.engine.Move(ctx, tx, workflow.MoveInput{CaseID: caseID, TokenID: tok.ID, Action: action, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: actor}})
		return err
	})
	// The rejected move was rolled back with its form values; its record goes
	// in a transaction of its own so it survives.
	if kind, ok := workflow.FailureKindOf(err); ok {
		rerr := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := s.engine.RecordFailure(ctx, tx, workflow.FailureInput{
				CaseID: caseID, TokenID: moved, Actor: workflow.Actor{Kind: workflow.ActorHuman, ID: actor}, Action: action, Kind: kind,
			})
			return err
		})
		if rerr != nil {
			return nil, errors.Join(err, rerr)
		}
	}
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
	events, err := s.engine.History(ctx, s.db, c.ID)
	if err != nil {
		return false, err
	}
	// Move clears the assignment, so a past handler is only in the events.
	for i := range events {
		if events[i].Actor.ID == actor {
			return true, nil
		}
	}
	if c.Status != workflow.StatusOpen {
		return false, nil
	}
	tokens, err := s.engine.Tokens(ctx, s.db, c.ID)
	if err != nil {
		return false, err
	}
	for i := range tokens {
		t := &tokens[i]
		if t.Status != workflow.TokenActive {
			continue
		}
		if t.AssignedTo == actor {
			return true, nil
		}
		ok, err := domain.Holds(ctx, s.profiles, t.Eligible, actor)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
