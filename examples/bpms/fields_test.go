package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFieldRegistryRejectsDuplicateKeys(t *testing.T) {
	_, err := NewFieldRegistry(textField{}, textField{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"text"`)
}

func TestDefaultFieldTypesAreRegistered(t *testing.T) {
	reg, err := NewFieldRegistry(DefaultFieldTypes()...)
	require.NoError(t, err)

	for _, key := range []string{"text", "textarea", "email", "select"} {
		_, ok := reg.Lookup(key)
		assert.True(t, ok, key)
	}
	_, ok := reg.Lookup("rut")
	assert.False(t, ok)
}

func TestFieldTypeConfig(t *testing.T) {
	tests := []struct {
		name    string
		typ     FieldType
		cfg     string
		wantErr bool
	}{
		{"text without config", textField{}, ``, false},
		{"text with max length", textField{}, `{"maxLength": 10}`, false},
		{"text with negative max length", textField{}, `{"maxLength": -1}`, true},
		{"text with unknown option", textField{}, `{"pattern": "x"}`, true},
		{"email rejects any option", emailField{}, `{"maxLength": 10}`, true},
		{"select with options", selectField{}, `{"options": ["a", "b"]}`, false},
		{"select without options", selectField{}, ``, true},
		{"select with duplicate options", selectField{}, `{"options": ["a", "a"]}`, true},
		{"select with an empty option", selectField{}, `{"options": [""]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.typ.ValidateConfig(json.RawMessage(tt.cfg))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestFieldTypeValue(t *testing.T) {
	tests := []struct {
		name    string
		typ     FieldType
		cfg     string
		value   any
		wantErr bool
	}{
		{"text accepts a string", textField{}, ``, "hola", false},
		{"text rejects a number", textField{}, ``, 42.0, true},
		{"text enforces the default max length", textField{}, ``, string(make([]byte, 201)), true},
		{"text enforces a configured max length", textField{}, `{"maxLength": 3}`, "abcd", true},
		{"textarea allows long text", textareaField{}, ``, string(make([]byte, 1500)), false},
		{"email accepts an address", emailField{}, ``, "ana@example.cl", false},
		{"email rejects a display name", emailField{}, ``, "Ana <ana@example.cl>", true},
		{"email rejects garbage", emailField{}, ``, "ana", true},
		{"select accepts an option", selectField{}, `{"options": ["a", "b"]}`, "b", false},
		{"select rejects an unknown option", selectField{}, `{"options": ["a", "b"]}`, "c", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.typ.Validate(tt.value, json.RawMessage(tt.cfg))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
