package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
)

type FieldType interface {
	Key() string
	ValidateConfig(cfg json.RawMessage) error
	Validate(value any, cfg json.RawMessage) error
	CELType() *cel.Type
}

type FieldRegistry struct {
	types map[string]FieldType
}

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

func (r *FieldRegistry) Lookup(key string) (FieldType, bool) {
	t, ok := r.types[key]
	return t, ok
}

func DefaultFieldTypes() []FieldType {
	return []FieldType{textField{}, textareaField{}, emailField{}, selectField{}}
}

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

type textField struct{}

func (textField) Key() string        { return "text" }
func (textField) CELType() *cel.Type { return cel.StringType }
func (textField) ValidateConfig(cfg json.RawMessage) error {
	_, err := validateLengthConfig(cfg)
	return err
}
func (textField) Validate(v any, cfg json.RawMessage) error {
	return validateLength(v, cfg, 200)
}

type textareaField struct{}

func (textareaField) Key() string        { return "textarea" }
func (textareaField) CELType() *cel.Type { return cel.StringType }
func (textareaField) ValidateConfig(cfg json.RawMessage) error {
	_, err := validateLengthConfig(cfg)
	return err
}
func (textareaField) Validate(v any, cfg json.RawMessage) error {
	return validateLength(v, cfg, 2000)
}

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
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return errors.New("must be a single email address")
	}
	return nil
}

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
