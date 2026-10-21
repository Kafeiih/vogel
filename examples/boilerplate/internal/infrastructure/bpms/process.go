package bpms

import (
	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/document"
	"github.com/kafeiih/vogel/pgxtx"
	"github.com/kafeiih/vogel/workflow"
)

type Process struct {
	Effects     func(r *action.Registry, docs *document.Service, db pgxtx.DBTX) error
	Guards      func(e *workflow.Engine, docs *document.Service) error
	Definitions func() []workflow.Definition
}

// Add this service's effects, guards and Definitions here (docs/BPMS.md).
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
