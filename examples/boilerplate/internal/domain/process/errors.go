package process

import (
	"errors"
	"fmt"

	"github.com/kafeiih/vogel/request"
)

var (
	ErrProcessNotFound = errors.New("process not found")
	ErrNotHolder       = errors.New("actor does not hold the profile for this task")
	ErrNotClaimant     = errors.New("task is not claimed by this actor")
	ErrNoAccess        = errors.New("actor has no access to this case")
)

type FormError struct {
	Fields request.FieldErrors
}

func (e *FormError) Error() string { return fmt.Sprintf("invalid form: %d field(s)", len(e.Fields)) }
