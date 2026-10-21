package bpms

import (
	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

// Process is what a service adds to the engine; see docs/BPMS.md.
type Process struct {
	Effects     func(r *action.Registry, docs *document.Service, db pgxtx.DBTX) error
	Guards      func(e *workflow.Engine, docs *document.Service) error
	Definitions func() []workflow.Definition
}

// service is this service's Process. Add its effect handlers, guards and
// Definitions here: the API and the worker both build from it.
var service = Process{
	Effects: func(r *action.Registry, docs *document.Service, db pgxtx.DBTX) error {
		return nil
	},
	Guards: func(e *workflow.Engine, docs *document.Service) error {
		return nil
	},
	Definitions: func() []workflow.Definition {
		return nil
	},
}
