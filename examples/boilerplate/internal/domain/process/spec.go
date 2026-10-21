package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kafeiih/vogel/request"
	"github.com/kafeiih/vogel/workflow"
)

type ProcessSpec struct {
	Name        string           `json:"name"`
	Nodes       []NodeSpec       `json:"nodes"`
	Transitions []TransitionSpec `json:"transitions"`
}

type NodeSpec struct {
	ID       string       `json:"id"`
	Start    bool         `json:"start,omitempty"`
	Terminal bool         `json:"terminal,omitempty"`
	Profile  *ProfileSpec `json:"profile,omitempty"`
	Form     []FieldSpec  `json:"form,omitempty"`
}

type ProfileSpec struct {
	Position string `json:"position"`
	Unit     string `json:"unit,omitempty"`
}

type TransitionSpec struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
}

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

type Process struct {
	Spec       ProcessSpec
	Definition workflow.Definition
	Forms      map[string]*Form
}

func (p *Process) ValidateNode(nodeID string, values, caseData map[string]any) (map[string]any, error) {
	form, ok := p.Forms[nodeID]
	if !ok {
		if len(values) > 0 {
			errs := request.FieldErrors{}
			for k := range values {
				errs[k] = "is not a field of this form"
			}
			return nil, &FormError{Fields: errs}
		}
		return nil, nil
	}
	clean, errs := form.Validate(values, caseData)
	if len(errs) > 0 {
		return nil, &FormError{Fields: errs}
	}
	return clean, nil
}

func (p *Process) ProfiledNodes() []string {
	var ids []string
	for _, n := range p.Definition.Nodes {
		if !n.Eligible.IsZero() {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func CompileProcess(reg *FieldRegistry, spec ProcessSpec, version int) (*Process, error) {
	def := workflow.Definition{Name: spec.Name, Version: version}
	var allFields []FieldSpec
	for _, n := range spec.Nodes {
		if n.Terminal && len(n.Form) > 0 {
			return nil, fmt.Errorf("node %q: a terminal node cannot have a form", n.ID)
		}
		node := workflow.Node{ID: n.ID, Start: n.Start, Terminal: n.Terminal}
		if n.Profile != nil {
			if n.Start {
				return nil, fmt.Errorf("node %q: the start node is filled in by whoever opens the case and cannot name a profile", n.ID)
			}
			if n.Profile.Position == "" || n.Profile.Unit == "" {
				return nil, fmt.Errorf("node %q: a profile needs a position and a unit", n.ID)
			}
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
