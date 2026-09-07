package jobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
)

func TestExampleJobArgs_Kind(t *testing.T) {
	args := ExampleJobArgs{Message: "hello"}

	if got := args.Kind(); got != "example" {
		t.Errorf("Kind(): got %q, want %q", got, "example")
	}
}

func TestExampleWorker_Work(t *testing.T) {
	w := &ExampleWorker{}
	job := &river.Job[ExampleJobArgs]{
		Args: ExampleJobArgs{Message: "test message"},
	}

	err := w.Work(context.Background(), job)
	if err != nil {
		t.Errorf("Work(): unexpected error: %v", err)
	}
}

func TestExampleWorker_WorkEmptyMessage(t *testing.T) {
	w := &ExampleWorker{}
	job := &river.Job[ExampleJobArgs]{
		Args: ExampleJobArgs{Message: ""},
	}

	err := w.Work(context.Background(), job)
	if err != nil {
		t.Errorf("Work(): unexpected error for empty message: %v", err)
	}
}
