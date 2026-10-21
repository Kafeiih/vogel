package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kafeiih/vogel/workflow"
)

// consultaJSON is the inquiry process this example publishes at boot when
// no version of it exists yet.
//
//go:embed consulta.json
var consultaJSON []byte

// ProcessSpec is a process definition as an author writes it: the flow
// graph, who handles each node, and each node's form. It carries no version:
// the catalog assigns one when the spec is published.
type ProcessSpec struct {
	Name        string           `json:"name"`
	Nodes       []NodeSpec       `json:"nodes"`
	Transitions []TransitionSpec `json:"transitions"`
}

// NodeSpec is one node of a ProcessSpec.
type NodeSpec struct {
	ID       string `json:"id"`
	Start    bool   `json:"start,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
	// Profile is who handles the node, as a profile (a position, optionally
	// in a unit), never as a person. Who holds the profile is resolved live
	// through a ProfileResolver. A start node without a profile is filled in
	// by whoever opens the case.
	Profile *ProfileSpec `json:"profile,omitempty"`
	Form    []FieldSpec  `json:"form,omitempty"`
}

// ProfileSpec names a profile. An empty Unit means the case's own unit.
type ProfileSpec struct {
	Position string `json:"position"`
	Unit     string `json:"unit,omitempty"`
}

// TransitionSpec is one edge of a ProcessSpec.
type TransitionSpec struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
}

// ParseProcessSpec strictly decodes a spec. A "version" key is rejected
// because versions are assigned on publish, never written by an author.
func ParseProcessSpec(raw []byte) (ProcessSpec, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ProcessSpec{}, fmt.Errorf("parse process spec: %w", err)
	}
	if _, ok := probe["version"]; ok {
		return ProcessSpec{}, errors.New("parse process spec: version is assigned on publish and must not be set")
	}

	var spec ProcessSpec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return ProcessSpec{}, fmt.Errorf("parse process spec: %w", err)
	}
	return spec, nil
}

// Process is a published, compiled spec: the engine definition plus one
// compiled form per node that has fields.
type Process struct {
	Spec       ProcessSpec
	Definition workflow.Definition
	Forms      map[string]*Form
}

// CompileProcess validates spec as version and compiles it. It checks the
// graph (workflow.Definition.Validate), every field type and option against
// reg, and every visibleWhen rule. Any error means the spec cannot be
// published.
func CompileProcess(reg *FieldRegistry, spec ProcessSpec, version int) (*Process, error) {
	def := workflow.Definition{Name: spec.Name, Version: version}
	var allFields []FieldSpec
	for _, n := range spec.Nodes {
		if n.Terminal && len(n.Form) > 0 {
			return nil, fmt.Errorf("node %q: a terminal node cannot have a form", n.ID)
		}
		node := workflow.Node{ID: n.ID, Start: n.Start, Terminal: n.Terminal}
		if n.Profile != nil {
			node.Eligible = workflow.ByPositionInUnit(n.Profile.Position, n.Profile.Unit)
		}
		def.Nodes = append(def.Nodes, node)
		allFields = append(allFields, n.Form...)
	}
	for _, t := range spec.Transitions {
		def.Transitions = append(def.Transitions, workflow.Transition{From: t.From, To: t.To, Action: t.Action})
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}

	p := &Process{Spec: spec, Definition: def, Forms: make(map[string]*Form)}
	for _, n := range spec.Nodes {
		if len(n.Form) == 0 {
			continue
		}
		form, err := CompileForm(reg, allFields, n.Form)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.ID, err)
		}
		p.Forms[n.ID] = form
	}
	return p, nil
}
