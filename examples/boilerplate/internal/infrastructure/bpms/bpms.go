package bpms

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/document/gotenberg"
	docpostgres "github.com/kafeiih/vogel/document/postgres"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/storage"
	"github.com/kafeiih/vogel/worker"
	"github.com/kafeiih/vogel/workflow"
	wfpostgres "github.com/kafeiih/vogel/workflow/postgres"
)

type Deps struct {
	// DB is where effect handlers write outside a request, typically the pool.
	DB            pgxtx.DBTX
	Queue         worker.Queue
	Storage       storage.Storage
	Gotenberg     gotenberg.Config
	WrapTransport func(http.RoundTripper) http.RoundTripper
	Logger        *slog.Logger
}

type BPMS struct {
	Registry  *action.Registry
	Engine    *workflow.Engine
	Documents *document.Service
}

// New builds the API (dispatch) and worker (run) sides from the same Process,
// so both registries hold the same effects.
func New(d Deps) (*BPMS, error) {
	return build(d, service)
}

func build(d Deps, p Process) (*BPMS, error) {
	gcfg := d.Gotenberg
	if d.WrapTransport != nil {
		gcfg.HTTPClient = &http.Client{Transport: d.WrapTransport(http.DefaultTransport)}
	}
	renderer, err := gotenberg.New(gcfg)
	if err != nil {
		return nil, fmt.Errorf("bpms: %w", err)
	}
	docs := document.NewService(docpostgres.NewRepository(), d.Storage, renderer, document.Config{}, d.Logger)

	registry := action.NewRegistry(d.Queue)
	if p.Effects != nil {
		if err := p.Effects(registry, docs, d.DB); err != nil {
			return nil, fmt.Errorf("bpms: registering effects: %w", err)
		}
	}

	engine := workflow.New(wfpostgres.NewRepository(), workflow.Config{}, d.Logger, workflow.WithEffectSink(registry))
	if p.Guards != nil {
		if err := p.Guards(engine, docs); err != nil {
			return nil, fmt.Errorf("bpms: registering guards: %w", err)
		}
	}
	if p.Definitions != nil {
		for _, def := range p.Definitions() {
			if err := engine.Register(def); err != nil {
				return nil, fmt.Errorf("bpms: %w", err)
			}
		}
	}

	return &BPMS{Registry: registry, Engine: engine, Documents: docs}, nil
}
