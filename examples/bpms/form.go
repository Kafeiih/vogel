package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"cel.dev/cel-go/cel"

	"github.com/kafeiih/vogel/request"
)

// FieldSpec is one field of a node's form, as written in a process
// definition.
type FieldSpec struct {
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	Label    string          `json:"label"`
	Required bool            `json:"required,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	// VisibleWhen is a CEL expression over the definition's fields. A field
	// whose rule is false is not validated and not stored. Empty means
	// always visible.
	VisibleWhen string `json:"visibleWhen,omitempty"`
}

type compiledField struct {
	spec    FieldSpec
	typ     FieldType
	visible cel.Program // nil: always visible
}

// Form is a node's form compiled against a FieldRegistry. Its rules can read
// every field of the definition, so a later node can depend on what an
// earlier node collected.
type Form struct {
	fields []compiledField
	// zero holds every definition field at its empty value, so a rule
	// never fails on a field nobody has filled in yet.
	zero map[string]any
}

// CompileForm compiles the form made of nodeFields. allFields is every field
// of the definition: it fixes the variables visibleWhen rules may use. Every
// error here is a publish-time error.
func CompileForm(reg *FieldRegistry, allFields, nodeFields []FieldSpec) (*Form, error) {
	env, zero, err := ruleEnv(reg, allFields)
	if err != nil {
		return nil, err
	}

	form := &Form{zero: zero}
	seen := make(map[string]bool, len(nodeFields))
	for _, spec := range nodeFields {
		if spec.Key == "" {
			return nil, errors.New("field without key")
		}
		if seen[spec.Key] {
			return nil, fmt.Errorf("field %q declared twice", spec.Key)
		}
		seen[spec.Key] = true

		typ, ok := reg.Lookup(spec.Type)
		if !ok {
			return nil, fmt.Errorf("field %q: unknown type %q", spec.Key, spec.Type)
		}
		if err := typ.ValidateConfig(spec.Config); err != nil {
			return nil, fmt.Errorf("field %q: %w", spec.Key, err)
		}

		cf := compiledField{spec: spec, typ: typ}
		if spec.VisibleWhen != "" {
			prg, err := compileRule(env, spec.VisibleWhen)
			if err != nil {
				return nil, fmt.Errorf("field %q: visibleWhen: %w", spec.Key, err)
			}
			cf.visible = prg
		}
		form.fields = append(form.fields, cf)
	}
	return form, nil
}

// ruleEnv declares one CEL variable per definition field, typed by its
// field type, and returns each field's empty value.
func ruleEnv(reg *FieldRegistry, allFields []FieldSpec) (*cel.Env, map[string]any, error) {
	opts := make([]cel.EnvOption, 0, len(allFields))
	zero := make(map[string]any, len(allFields))
	for _, f := range allFields {
		typ, ok := reg.Lookup(f.Type)
		if !ok {
			return nil, nil, fmt.Errorf("field %q: unknown type %q", f.Key, f.Type)
		}
		if _, dup := zero[f.Key]; dup {
			return nil, nil, fmt.Errorf("field %q declared twice in the definition", f.Key)
		}
		opts = append(opts, cel.Variable(f.Key, typ.CELType()))
		// Every built-in type is a string; a non-string type would supply
		// its own empty value here.
		zero[f.Key] = ""
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("build rule environment: %w", err)
	}
	return env, zero, nil
}

// compileRule compiles expr and requires it to produce a bool.
func compileRule(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("must evaluate to bool, got %s", ast.OutputType())
	}
	return env.Program(ast)
}

// Validate checks a submission. caseData is what the case already holds;
// submitted values override it when rules are evaluated. It returns the
// values to store (visible fields only) or one error per offending field.
func (f *Form) Validate(submitted, caseData map[string]any) (map[string]any, request.FieldErrors) {
	errs := request.FieldErrors{}

	vars := make(map[string]any, len(f.zero))
	for k, v := range f.zero {
		vars[k] = v
	}
	for k, v := range caseData {
		if _, declared := f.zero[k]; declared {
			vars[k] = v
		}
	}

	declared := make(map[string]bool, len(f.fields))
	for _, cf := range f.fields {
		declared[cf.spec.Key] = true
		if v, ok := submitted[cf.spec.Key]; ok {
			if _, isString := v.(string); isString {
				vars[cf.spec.Key] = v
			}
		}
	}
	for k := range submitted {
		if !declared[k] {
			errs[k] = "is not a field of this form"
		}
	}

	out := make(map[string]any, len(f.fields))
	for _, cf := range f.fields {
		key := cf.spec.Key
		if cf.visible != nil {
			res, _, err := cf.visible.Eval(vars)
			if err != nil {
				errs[key] = "visibility rule failed"
				continue
			}
			if res.Value() != true {
				continue
			}
		}

		v, present := submitted[key]
		if !present || v == nil || v == "" {
			if cf.spec.Required {
				errs[key] = "is required"
			}
			continue
		}
		if err := cf.typ.Validate(v, cf.spec.Config); err != nil {
			errs[key] = err.Error()
			continue
		}
		out[key] = v
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}
