package workflow

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
)

// walkGen builds a random block-structured Definition: a start task, a
// sequence of tasks and nested parallel or inclusive blocks, and a terminal
// node. Tasks get random eligibilities and deadlines, and some get a return
// transition to an earlier task of the same sequence (so it never crosses a
// block boundary).
type walkGen struct {
	r     *rand.Rand
	n     int
	nodes []Node
	trs   []Transition
}

func (g *walkGen) id(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

// seq adds a sequence of one to three elements leading from `from` to `to`.
// The first transition leaving `from` carries guard.
func (g *walkGen) seq(from, to string, depth int, guard string) {
	cur, action := from, g.id("go")
	var tasks []string
	for i, n := 0, 1+g.r.Intn(3); i < n; i++ {
		if depth < 2 && g.r.Intn(3) == 0 {
			split, join := g.id("split"), g.id("join")
			kind := NodeParallel
			if g.r.Intn(2) == 0 {
				kind = NodeInclusive
			}
			g.nodes = append(g.nodes, Node{ID: split, Kind: kind, Join: join}, Node{ID: join, Kind: NodeJoin})
			g.trs = append(g.trs, Transition{From: cur, To: split, Action: action, Guard: guard})
			branches := 2 + g.r.Intn(2)
			for b := 0; b < branches; b++ {
				bguard := ""
				if kind == NodeInclusive && b < branches-1 {
					bguard = "coin"
				}
				if b == branches-1 && g.r.Intn(3) == 0 {
					g.trs = append(g.trs, Transition{From: split, To: join, Action: g.id("skip")})
					continue
				}
				g.seq(split, join, depth+1, bguard)
			}
			tasks = nil
			cur, action, guard = join, g.id("go"), ""
			continue
		}
		task := g.id("task")
		node := Node{ID: task, Eligible: ByPosition(fmt.Sprintf("pos%d", g.r.Intn(3)))}
		if g.r.Intn(2) == 0 {
			node.Deadline = time.Duration(1+g.r.Intn(48)) * time.Hour
		}
		g.nodes = append(g.nodes, node)
		g.trs = append(g.trs, Transition{From: cur, To: task, Action: action, Guard: guard})
		if len(tasks) > 0 && g.r.Intn(3) == 0 {
			g.trs = append(g.trs, Transition{From: task, To: tasks[g.r.Intn(len(tasks))], Action: g.id("back"), Return: true})
		}
		tasks = append(tasks, task)
		cur, action, guard = task, g.id("go"), ""
	}
	g.trs = append(g.trs, Transition{From: cur, To: to, Action: action, Guard: guard})
}

func randomDefinition(r *rand.Rand, version int) Definition {
	g := &walkGen{r: r}
	g.nodes = []Node{{ID: "start", Start: true}, {ID: "end", Terminal: true}}
	g.seq("start", "end", 0, "")
	return Definition{Name: "walk", Version: version, Nodes: g.nodes, Transitions: g.trs}
}

// TestEngine_RandomWalk_InvariantsHold drives cases of generated definitions
// with seeded random moves, claims, releases and cancellations, asserting
// I1-I8 (assertInvariants) after every committed call.
func TestEngine_RandomWalk_InvariantsHold(t *testing.T) {
	const definitions, casesPerDefinition, maxSteps = 40, 3, 60
	for seed := int64(1); seed <= definitions; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			d := randomDefinition(r, int(seed))
			require.NoError(t, d.Validate(), "generated definitions are block-structured")

			repo := newFakeRepository()
			e := New(repo, Config{}, testLogger())
			require.NoError(t, e.Register(d))
			require.NoError(t, e.RegisterGuard("coin", func(context.Context, pgxtx.DBTX, Case) (bool, error) {
				return r.Intn(2) == 0, nil
			}))

			for n := 0; n < casesPerDefinition; n++ {
				walkCase(t, r, e, repo, d, maxSteps)
			}
		})
	}
}

func walkCase(t *testing.T, r *rand.Rand, e *Engine, repo *fakeRepository, d Definition, maxSteps int) {
	ctx := context.Background()
	c, err := e.Open(ctx, nil, OpenInput{Definition: d.Name, Version: d.Version, Domain: "walk", ExternalID: uuid.NewString(), Unit: "hq", ActorID: "u1"})
	require.NoError(t, err)
	assertInvariants(t, e, repo, c.ID)

	for step := 0; step < maxSteps; step++ {
		got, err := repo.GetByID(ctx, nil, c.ID)
		require.NoError(t, err)
		if got.Status != StatusOpen {
			return
		}
		var active []Token
		tokens, err := e.Tokens(ctx, nil, c.ID)
		require.NoError(t, err)
		for _, tok := range tokens {
			if tok.Status == TokenActive {
				active = append(active, tok)
			}
		}
		require.NotEmpty(t, active)
		tok := active[r.Intn(len(active))]
		actor := Actor{ID: fmt.Sprintf("p%d", r.Intn(3))}

		switch roll := r.Intn(20); {
		case roll < 13:
			trs, err := e.AvailableFor(ctx, nil, c.ID, tok.ID)
			require.NoError(t, err)
			require.NotEmpty(t, trs, "a task node always offers a transition")
			tr := trs[r.Intn(len(trs))]
			_, err = e.Move(ctx, nil, MoveInput{CaseID: c.ID, TokenID: tok.ID, Action: tr.Action, Actor: actor})
			require.NoError(t, err, "move %s from %s", tr.Action, tok.Node)
		case roll < 15:
			_, err = e.ClaimWith(ctx, nil, ClaimInput{CaseID: c.ID, TokenID: tok.ID, Actor: actor})
			if !errors.Is(err, ErrAlreadyAssigned) {
				require.NoError(t, err)
			}
		case roll < 17:
			_, err = e.ReleaseWith(ctx, nil, ReleaseInput{CaseID: c.ID, TokenID: tok.ID, Actor: actor, Reason: "walk"})
			if !errors.Is(err, ErrNotAssigned) {
				require.NoError(t, err)
			}
		case roll < 19:
			_, err = e.CancelToken(ctx, nil, CancelTokenInput{CaseID: c.ID, TokenID: tok.ID, Actor: actor, Reason: "walk"})
			if !errors.Is(err, ErrLastBranch) {
				require.NoError(t, err)
			}
		default:
			if r.Intn(4) == 0 {
				_, err = e.Cancel(ctx, nil, CancelInput{CaseID: c.ID, Actor: actor, Reason: "walk"})
				require.NoError(t, err)
			}
		}
		assertInvariants(t, e, repo, c.ID)
	}
}
