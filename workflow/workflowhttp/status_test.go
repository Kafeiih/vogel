package workflowhttp

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/workflow"
)

// sentinel is one exported Err* of package workflow and the status Status
// must report for it; status 0 means "not mapped".
type sentinel struct {
	name   string
	err    error
	status int
	code   string
}

// sentinels is the hand-maintained list of every exported Err* variable of
// package workflow. TestStatus_ListMatchesWorkflowPackage fails when a
// sentinel is added to (or removed from) the package without updating it, so
// a new sentinel is always classified on purpose instead of silently
// becoming a 500.
var sentinels = []sentinel{
	{"ErrCaseNotFound", workflow.ErrCaseNotFound, http.StatusNotFound, response.CodeNotFound},
	{"ErrTokenNotFound", workflow.ErrTokenNotFound, http.StatusNotFound, response.CodeNotFound},

	{"ErrConflict", workflow.ErrConflict, http.StatusConflict, response.CodeConflict},
	{"ErrAlreadyAssigned", workflow.ErrAlreadyAssigned, http.StatusConflict, response.CodeConflict},
	{"ErrCaseClosed", workflow.ErrCaseClosed, http.StatusConflict, response.CodeConflict},
	{"ErrInvalidTransition", workflow.ErrInvalidTransition, http.StatusConflict, response.CodeConflict},
	{"ErrReturnNotVisited", workflow.ErrReturnNotVisited, http.StatusConflict, response.CodeConflict},
	{"ErrNoRoute", workflow.ErrNoRoute, http.StatusConflict, response.CodeConflict},
	{"ErrGuardRejected", workflow.ErrGuardRejected, http.StatusConflict, response.CodeConflict},
	{"ErrLastBranch", workflow.ErrLastBranch, http.StatusConflict, response.CodeConflict},
	{"ErrJoinRejected", workflow.ErrJoinRejected, http.StatusConflict, response.CodeConflict},
	{"ErrAlreadyExpired", workflow.ErrAlreadyExpired, http.StatusConflict, response.CodeConflict},
	{"ErrNotOverdue", workflow.ErrNotOverdue, http.StatusConflict, response.CodeConflict},

	{"ErrCommentRequired", workflow.ErrCommentRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{"ErrCommentNotAllowed", workflow.ErrCommentNotAllowed, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{"ErrReasonRequired", workflow.ErrReasonRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{"ErrTokenRequired", workflow.ErrTokenRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{"ErrInvalidActor", workflow.ErrInvalidActor, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},

	{"ErrOffsetOutOfRange", workflow.ErrOffsetOutOfRange, http.StatusBadRequest, response.CodeValidationError},

	// Not mapped: the caller logs them and answers 500.
	{"ErrDefinitionNotRegistered", workflow.ErrDefinitionNotRegistered, 0, ""},
	{"ErrGuardNotRegistered", workflow.ErrGuardNotRegistered, 0, ""},
	{"ErrCaseExists", workflow.ErrCaseExists, 0, ""},
	{"ErrNotAssigned", workflow.ErrNotAssigned, 0, ""},
	{"ErrInvalidDefinition", workflow.ErrInvalidDefinition, 0, ""},
	{"ErrTooManyDecisionHops", workflow.ErrTooManyDecisionHops, 0, ""},
	{"ErrUnknownEffect", workflow.ErrUnknownEffect, 0, ""},
	{"ErrUnsupportedEventSchema", workflow.ErrUnsupportedEventSchema, 0, ""},
	{"ErrInvalidFailure", workflow.ErrInvalidFailure, 0, ""},
	{"ErrChainBroken", workflow.ErrChainBroken, 0, ""},
}

// workflowSentinelNames parses package workflow's non-test sources and
// returns the name of every exported package-level variable starting with
// "Err".
func workflowSentinelNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("..")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join("..", name), nil, 0)
		require.NoError(t, err)
		require.Equal(t, "workflow", file.Name.Name, name)
		files = append(files, file)
	}
	require.NotEmpty(t, files, "no workflow sources found in ..")

	var names []string
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					if id.IsExported() && strings.HasPrefix(id.Name, "Err") {
						names = append(names, id.Name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestStatus_ListMatchesWorkflowPackage(t *testing.T) {
	listed := make([]string, 0, len(sentinels))
	for _, s := range sentinels {
		listed = append(listed, s.name)
	}
	sort.Strings(listed)
	assert.Equal(t, workflowSentinelNames(t), listed,
		"classify every exported workflow Err* in sentinels (mapped or not) and in Status")
}

func TestStatus_MapsEveryWorkflowSentinel(t *testing.T) {
	for _, s := range sentinels {
		t.Run(s.name, func(t *testing.T) {
			status, code, ok := Status(s.err)
			if s.status == 0 {
				assert.False(t, ok, "%s must not be mapped", s.name)
				return
			}
			require.True(t, ok, "%s must be mapped", s.name)
			assert.Equal(t, s.status, status)
			assert.Equal(t, s.code, code)
		})
	}
}

func TestStatus_WrappedErrorsAreMapped(t *testing.T) {
	err := fmt.Errorf("workflow: move case x: %w", workflow.ErrNoRoute)
	status, code, ok := Status(fmt.Errorf("handler: %w", err))
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, response.CodeConflict, code)

	status, _, ok = Status(fmt.Errorf("%w: %w", workflow.ErrJoinRejected, errors.New("branch values clash")))
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, status)
}

func TestStatus_UnknownErrorIsNotMapped(t *testing.T) {
	for _, err := range []error{nil, errors.New("connection reset"), fmt.Errorf("register: %w", workflow.ErrInvalidDefinition)} {
		status, code, ok := Status(err)
		assert.False(t, ok, "%v", err)
		assert.Zero(t, status)
		assert.Empty(t, code)
	}
}
