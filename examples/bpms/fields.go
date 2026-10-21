package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
)

// FieldType is the contract every field type implements. The engine never
// sees it: a form is compiled against a FieldRegistry when a definition is
// published, and a submission is validated against it before the case moves.
// Adding a type means writing one of these and registering it; neither the
// engine nor any published definition changes.
type FieldType interface {
	// Key is the value of FieldSpec.Type that selects this type.
	Key() string
	// ValidateConfig checks a field's options. It runs when a definition is
	// published, so a bad option never reaches a citizen.
	ValidateConfig(cfg json.RawMessage) error
	// Validate checks one submitted, non-empty value against the field's
	// options. Required-ness is checked by the form, not here.
	Validate(value any, cfg json.RawMessage) error
	// CELType is the type visibleWhen rules see for this field.
	CELType() *cel.Type
}

// FieldRegistry holds the field types a definition may use, by key.
type FieldRegistry struct {
	types map[string]FieldType
}

// NewFieldRegistry registers types and rejects two types with the same key.
func NewFieldRegistry(types ...FieldType) (*FieldRegistry, error) {
	r := &FieldRegistry{types: make(map[string]FieldType, len(types))}
	for _, t := range types {
		if _, dup := r.types[t.Key()]; dup {
			return nil, fmt.Errorf("field type %q registered twice", t.Key())
		}
		r.types[t.Key()] = t
	}
	return r, nil
}

// Lookup returns the type registered under key.
func (r *FieldRegistry) Lookup(key string) (FieldType, bool) {
	t, ok := r.types[key]
	return t, ok
}

// DefaultFieldTypes returns the field types this example ships with.
func DefaultFieldTypes() []FieldType {
	return []FieldType{textField{}, textareaField{}, emailField{}, selectField{}}
}

// decodeConfig strictly decodes a field's options into dst; an absent config
// decodes as an empty object, and an unknown option is an error.
func decodeConfig(cfg json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(cfg)) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}

var errNotString = errors.New("must be a string")

// lengthConfig is the option set shared by text and textarea.
type lengthConfig struct {
	MaxLength int `json:"maxLength"`
}

func validateLengthConfig(cfg json.RawMessage) (lengthConfig, error) {
	var c lengthConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	if c.MaxLength < 0 {
		return c, errors.New("maxLength must not be negative")
	}
	return c, nil
}

func validateLength(value any, cfg json.RawMessage, defaultMax int) error {
	s, ok := value.(string)
	if !ok {
		return errNotString
	}
	c, err := validateLengthConfig(cfg)
	if err != nil {
		return err
	}
	limit := c.MaxLength
	if limit == 0 {
		limit = defaultMax
	}
	if utf8.RuneCountInString(s) > limit {
		return fmt.Errorf("must be at most %d characters", limit)
	}
	return nil
}

// textField is a single line of text, 200 characters unless maxLength says
// otherwise.
type textField struct{}

func (textField) Key() string                              { return "text" }
func (textField) CELType() *cel.Type                       { return cel.StringType }
func (textField) ValidateConfig(cfg json.RawMessage) error { _, err := validateLengthConfig(cfg); return err }
func (textField) Validate(v any, cfg json.RawMessage) error {
	return validateLength(v, cfg, 200)
}

// textareaField is free text, 2000 characters unless maxLength says
// otherwise.
type textareaField struct{}

func (textareaField) Key() string                              { return "textarea" }
func (textareaField) CELType() *cel.Type                       { return cel.StringType }
func (textareaField) ValidateConfig(cfg json.RawMessage) error { _, err := validateLengthConfig(cfg); return err }
func (textareaField) Validate(v any, cfg json.RawMessage) error {
	return validateLength(v, cfg, 2000)
}

// emailField is one bare email address. It takes no options.
type emailField struct{}

func (emailField) Key() string        { return "email" }
func (emailField) CELType() *cel.Type { return cel.StringType }
func (emailField) ValidateConfig(cfg json.RawMessage) error {
	return decodeConfig(cfg, &struct{}{})
}
func (emailField) Validate(v any, _ json.RawMessage) error {
	s, ok := v.(string)
	if !ok {
		return errNotString
	}
	// ParseAddress also accepts "Name <addr>"; requiring the parsed address
	// to equal the input keeps only bare addresses.
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return errors.New("must be a single email address")
	}
	return nil
}

// selectField is one value out of a fixed, non-empty list of options.
type selectField struct{}

type selectConfig struct {
	Options []string `json:"options"`
}

func (selectField) Key() string        { return "select" }
func (selectField) CELType() *cel.Type { return cel.StringType }
func (selectField) ValidateConfig(cfg json.RawMessage) error {
	var c selectConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return err
	}
	if len(c.Options) == 0 {
		return errors.New("options must not be empty")
	}
	seen := make(map[string]bool, len(c.Options))
	for _, o := range c.Options {
		if o == "" {
			return errors.New("options must not contain an empty value")
		}
		if seen[o] {
			return fmt.Errorf("option %q is repeated", o)
		}
		seen[o] = true
	}
	return nil
}
func (selectField) Validate(v any, cfg json.RawMessage) error {
	s, ok := v.(string)
	if !ok {
		return errNotString
	}
	var c selectConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return err
	}
	for _, o := range c.Options {
		if o == s {
			return nil
		}
	}
	return errors.New("must be one of the configured options")
}
