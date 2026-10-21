package bpms

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/document/gotenberg"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

func testDeps() Deps {
	return Deps{Gotenberg: gotenberg.Config{BaseURL: "http://gotenberg:3000"}}
}

func noop(context.Context, action.Run) error { return nil }

func definitionWithEffect(effect string) workflow.Definition {
	return workflow.Definition{
		Name:    "request",
		Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Eligible: workflow.ByPosition("citizen")},
			{ID: "done", Terminal: true, Effects: []string{effect}},
		},
		Transitions: []workflow.Transition{
			{From: "draft", To: "done", Action: "submit", Guard: "always"},
		},
	}
}

func testProcess(effect string) Process {
	return Process{
		Effects: func(r *action.Registry, _ *document.Service, _ pgxtx.DBTX) error {
			return r.Register("notify-citizen", action.HandlerFunc(noop))
		},
		Guards: func(e *workflow.Engine, _ *document.Service) error {
			return e.RegisterGuard("always", func(context.Context, pgxtx.DBTX, workflow.Case) (bool, error) { return true, nil })
		},
		Definitions: func() []workflow.Definition {
			return []workflow.Definition{definitionWithEffect(effect)}
		},
	}
}

func TestNew_RegistersDefinitionNamingARegisteredEffect(t *testing.T) {
	b, err := build(testDeps(), testProcess("notify-citizen"))
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	if !b.Registry.HasEffect("notify-citizen") {
		t.Fatal("registry does not know notify-citizen")
	}
	defs := b.Engine.Definitions()
	if len(defs) != 1 || defs[0].Key() != "request@1" {
		t.Fatalf("Definitions() = %v, want request@1", defs)
	}
	if b.Documents == nil {
		t.Fatal("Documents is nil")
	}
}

func TestNew_RejectsDefinitionNamingAnUnknownEffect(t *testing.T) {
	_, err := build(testDeps(), testProcess("missing"))
	if !errors.Is(err, workflow.ErrUnknownEffect) {
		t.Fatalf("build() error = %v, want workflow.ErrUnknownEffect", err)
	}
}

func TestNew_PropagatesRegistrationErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, p := range map[string]Process{
		"effects": {Effects: func(*action.Registry, *document.Service, pgxtx.DBTX) error { return boom }},
		"guards":  {Guards: func(*workflow.Engine, *document.Service) error { return boom }},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build(testDeps(), p); !errors.Is(err, boom) {
				t.Fatalf("build() error = %v, want %v", err, boom)
			}
		})
	}
}

func TestNew_RejectsAnInvalidGotenbergURL(t *testing.T) {
	d := testDeps()
	d.Gotenberg.BaseURL = "gotenberg:3000"
	if _, err := New(d); err == nil {
		t.Fatal("New() error = nil, want an error for a relative Gotenberg URL")
	}
}

func TestNew_WrapsTheGotenbergTransport(t *testing.T) {
	d := testDeps()
	wrapped := false
	d.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		wrapped = true
		return rt
	}
	if _, err := New(d); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !wrapped {
		t.Fatal("WrapTransport was not called")
	}
}

func TestNew_ServiceProcessBuildsOnBothSides(t *testing.T) {
	api, err := New(testDeps())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	wrk, err := New(testDeps())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if len(api.Engine.Definitions()) != len(wrk.Engine.Definitions()) {
		t.Fatal("api and worker built different definitions")
	}
}
