package workflow

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// TokenStatus is the lifecycle state of a Token.
type TokenStatus string

const (
	// TokenActive rests on a task node, waiting for a human action.
	TokenActive TokenStatus = "active"
	// TokenWaiting arrived at its scope's join and waits for its siblings.
	TokenWaiting TokenStatus = "waiting"
	// TokenConsumed left the case: a split or a join consumed it, or it
	// reached a terminal node.
	TokenConsumed TokenStatus = "consumed"
	// TokenCancelled was cancelled (Engine.Cancel, Engine.CancelToken).
	TokenCancelled TokenStatus = "cancelled"
)

// Live reports whether a token in status s still counts as a branch of its
// scope: TokenActive or TokenWaiting.
func (s TokenStatus) Live() bool {
	return s == TokenActive || s == TokenWaiting
}

// Token is one thread of execution inside a case: the unit a person claims,
// works on and submits (a work item). A case that never split has exactly one
// token, whose ID equals the case ID; a split consumes the arriving token and
// emits one token per taken branch, and a join consumes the branches' tokens
// and emits one token in the enclosing scope. A token keeps its ID while it
// moves from node to node inside its scope.
//
// Tokens are mutable state, like Case: the history of how they got there is
// the case's events, each of which names its Event.TokenID and Event.ScopeID.
type Token struct {
	ID      uuid.UUID
	CaseID  uuid.UUID
	ScopeID uuid.UUID
	// Node is the node the token rests on (TokenActive), waits at (a join,
	// TokenWaiting), or last occupied (TokenConsumed, TokenCancelled).
	Node   string
	Status TokenStatus
	// Eligible is Node's eligibility with ResolveUnit already applied
	// against the case's unit, stored so an inbox can filter by it.
	Eligible   Eligibility
	AssignedTo string
	// DeadlineAt is Node.Deadline applied when the token entered Node, or the
	// deadline Engine.SetDeadline set since.
	DeadlineAt *time.Time
	// ExpiredDeadline is the deadline Engine.Expire recorded as expired. The
	// token counts as expired while it equals DeadlineAt; entering a node and
	// Engine.SetDeadline clear it.
	ExpiredDeadline *time.Time
	// EnteredSeq and EnteredAt are the Seq and time of the event that moved
	// the token onto Node.
	EnteredSeq int64
	EnteredAt  time.Time
	// EndedSeq and EndedAt are set when the token stops being live
	// (consumed or cancelled); EndedSeq is 0 and EndedAt nil before.
	EndedSeq int64
	EndedAt  *time.Time
}

// ScopeStatus is the lifecycle state of a Scope.
type ScopeStatus string

const (
	ScopeOpen      ScopeStatus = "open"
	ScopeJoined    ScopeStatus = "joined"
	ScopeCancelled ScopeStatus = "cancelled"
	// ScopeClosed is the status of a case's root scope once the case closed.
	ScopeClosed ScopeStatus = "closed"
)

// Scope groups the tokens one split emitted until its join consumes them.
// Every case has a root scope whose ID equals the case ID (ParentID nil, no
// split or join node, Expected 1); each split opens a child scope of the
// arriving token's scope.
type Scope struct {
	ID       uuid.UUID
	CaseID   uuid.UUID
	ParentID *uuid.UUID
	// SplitNode and JoinNode are the block's split and join; empty for the
	// root scope.
	SplitNode string
	JoinNode  string
	// Expected is how many tokens the split emitted: the join fires when
	// waiting plus cancelled tokens reach it.
	Expected  int
	Status    ScopeStatus
	OpenedSeq int64
	// ClosedSeq is the Seq of the event that joined, cancelled or closed the
	// scope; 0 while it is open.
	ClosedSeq int64
}

// IsRoot reports whether s is its case's root scope.
func (s Scope) IsRoot() bool {
	return s.ParentID == nil
}

// WorkItem is one Inbox row: an active token and the case it belongs to.
type WorkItem struct {
	Case  Case
	Token Token
}

// JoinRecord describes one join a call fired: the scope as joined and the
// tokens it consumed (see Config.JoinCheck and MoveResult.Joined).
type JoinRecord struct {
	Scope  Scope
	Tokens []Token
}

// SplitDetail is the Event.Detail of EventSplit: the scope the split opened,
// the split node and the tokens it emitted, in branch order.
type SplitDetail struct {
	Scope     uuid.UUID   `json:"scope"`
	SplitNode string      `json:"split_node"`
	Tokens    []uuid.UUID `json:"tokens"`
}

// JoinDetail is the Event.Detail of EventJoined: the scope that joined, its
// join node and the tokens the join consumed.
type JoinDetail struct {
	Scope    uuid.UUID   `json:"scope"`
	JoinNode string      `json:"join_node"`
	Tokens   []uuid.UUID `json:"tokens"`
}

// CancelDetail is the Event.Detail of EventTokenCancelled and EventCancelled:
// the caller's reason.
type CancelDetail struct {
	Reason string `json:"reason"`
}

// Sentinel errors of the token model.
var (
	ErrTokenRequired  = errors.New("workflow: case has several active tokens; name one")
	ErrTokenNotFound  = errors.New("workflow: token is not active in this case")
	ErrLastBranch     = errors.New("workflow: cancelling this token would leave its scope without a live branch")
	ErrJoinRejected   = errors.New("workflow: join rejected by the consumer's join check")
	ErrReasonRequired = errors.New("workflow: a reason is required")
)
