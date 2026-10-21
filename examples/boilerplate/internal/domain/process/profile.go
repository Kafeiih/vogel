package process

import (
	"context"
	"slices"

	"github.com/kafeiih/vogel/workflow"
)

type ProfileResolver interface {
	Holders(ctx context.Context, position, unit string) ([]string, error)
}

func Holds(ctx context.Context, r ProfileResolver, e workflow.Eligibility, actor string) (bool, error) {
	if e.IsZero() {
		return false, nil
	}
	people, err := r.Holders(ctx, e.Position, e.Unit)
	if err != nil {
		return false, err
	}
	return slices.Contains(people, actor), nil
}
