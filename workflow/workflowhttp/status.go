// Package workflowhttp maps the errors of vogel/workflow to HTTP status codes
// and the error codes of vogel/httpx/response. It is a separate package so
// the workflow engine itself stays free of any HTTP dependency.
package workflowhttp

import (
	"errors"
	"net/http"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/workflow"
)

// mapping is one sentinel and the status and code Status reports for it.
type mapping struct {
	err    error
	status int
	code   string
}

// mappings lists every workflow sentinel a client can cause. Any error not
// listed here (a broken definition, an unknown effect, a database failure)
// is a server fault: Status reports ok = false for it.
var mappings = []mapping{
	{workflow.ErrCaseNotFound, http.StatusNotFound, response.CodeNotFound},
	{workflow.ErrTokenNotFound, http.StatusNotFound, response.CodeNotFound},

	{workflow.ErrConflict, http.StatusConflict, response.CodeConflict},
	{workflow.ErrAlreadyAssigned, http.StatusConflict, response.CodeConflict},
	{workflow.ErrCaseClosed, http.StatusConflict, response.CodeConflict},
	{workflow.ErrInvalidTransition, http.StatusConflict, response.CodeConflict},
	{workflow.ErrReturnNotVisited, http.StatusConflict, response.CodeConflict},
	{workflow.ErrNoRoute, http.StatusConflict, response.CodeConflict},
	{workflow.ErrGuardRejected, http.StatusConflict, response.CodeConflict},
	{workflow.ErrLastBranch, http.StatusConflict, response.CodeConflict},
	{workflow.ErrJoinRejected, http.StatusConflict, response.CodeConflict},
	{workflow.ErrAlreadyExpired, http.StatusConflict, response.CodeConflict},
	{workflow.ErrNotOverdue, http.StatusConflict, response.CodeConflict},

	{workflow.ErrCommentRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{workflow.ErrCommentNotAllowed, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{workflow.ErrReasonRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{workflow.ErrTokenRequired, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},
	{workflow.ErrInvalidActor, http.StatusUnprocessableEntity, response.CodeUnprocessableEntity},

	{workflow.ErrOffsetOutOfRange, http.StatusBadRequest, response.CodeValidationError},
}

// Status returns the HTTP status and response code for err when it wraps a
// workflow sentinel a client can cause (checked with errors.Is), and ok =
// false otherwise. A caller writes the mapped response when ok is true, and
// logs err and answers 500 when it is false:
//
//	if status, code, ok := workflowhttp.Status(err); ok {
//		response.Error(w, r, status, code, err.Error())
//		return
//	}
//
// The mapping is:
//
//   - 404 NOT_FOUND: ErrCaseNotFound, ErrTokenNotFound.
//   - 409 CONFLICT: ErrConflict, ErrAlreadyAssigned, ErrCaseClosed,
//     ErrInvalidTransition, ErrReturnNotVisited, ErrNoRoute,
//     ErrGuardRejected, ErrLastBranch, ErrJoinRejected, ErrAlreadyExpired,
//     ErrNotOverdue.
//   - 422 UNPROCESSABLE_ENTITY: ErrCommentRequired, ErrCommentNotAllowed,
//     ErrReasonRequired, ErrTokenRequired, ErrInvalidActor.
//   - 400 VALIDATION_ERROR: ErrOffsetOutOfRange.
//
// Every other error, ErrInvalidDefinition, ErrUnknownEffect and
// ErrTooManyDecisionHops included, is not mapped.
func Status(err error) (status int, code string, ok bool) {
	if err == nil {
		return 0, "", false
	}
	for _, m := range mappings {
		if errors.Is(err, m.err) {
			return m.status, m.code, true
		}
	}
	return 0, "", false
}
