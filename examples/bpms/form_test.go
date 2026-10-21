package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func consultaFields() []FieldSpec {
	return []FieldSpec{
		{Key: "nombre", Type: "text", Label: "Nombre", Required: true},
		{Key: "correo", Type: "email", Label: "Correo", Required: true},
		{Key: "tema", Type: "select", Label: "Tema", Required: true, Config: []byte(`{"options": ["general", "reclamo"]}`)},
		{Key: "detalle_reclamo", Type: "textarea", Label: "Detalle", Required: true, VisibleWhen: `tema == "reclamo"`},
	}
}

func testRegistry(t *testing.T) *FieldRegistry {
	t.Helper()
	reg, err := NewFieldRegistry(DefaultFieldTypes()...)
	require.NoError(t, err)
	return reg
}

func TestCompileFormRejectsAnUnknownType(t *testing.T) {
	fields := []FieldSpec{{Key: "rut", Type: "rut", Label: "RUT"}}
	_, err := CompileForm(testRegistry(t), fields, fields)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"rut"`)
}

func TestCompileFormRejectsDuplicateKeys(t *testing.T) {
	fields := []FieldSpec{{Key: "a", Type: "text"}, {Key: "a", Type: "text"}}
	_, err := CompileForm(testRegistry(t), fields, fields)
	assert.Error(t, err)
}

func TestCompileFormRejectsABadConfig(t *testing.T) {
	fields := []FieldSpec{{Key: "tema", Type: "select"}}
	_, err := CompileForm(testRegistry(t), fields, fields)
	assert.Error(t, err)
}

func TestCompileFormRejectsARuleOnAnUndeclaredField(t *testing.T) {
	fields := []FieldSpec{{Key: "a", Type: "text", VisibleWhen: `b == "x"`}}
	_, err := CompileForm(testRegistry(t), fields, fields)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "visibleWhen")
}

func TestCompileFormRejectsANonBooleanRule(t *testing.T) {
	fields := []FieldSpec{{Key: "a", Type: "text", VisibleWhen: `a + "x"`}}
	_, err := CompileForm(testRegistry(t), fields, fields)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bool")
}

func TestFormValidateAcceptsAValidSubmission(t *testing.T) {
	form, err := CompileForm(testRegistry(t), consultaFields(), consultaFields())
	require.NoError(t, err)

	got, errs := form.Validate(map[string]any{
		"nombre": "Ana", "correo": "ana@example.cl", "tema": "general",
	}, nil)

	assert.Empty(t, errs)
	assert.Equal(t, map[string]any{"nombre": "Ana", "correo": "ana@example.cl", "tema": "general"}, got)
}

func TestFormValidateReportsEveryFieldError(t *testing.T) {
	form, err := CompileForm(testRegistry(t), consultaFields(), consultaFields())
	require.NoError(t, err)

	_, errs := form.Validate(map[string]any{"correo": "ana", "tema": "otro", "extra": "x"}, nil)

	assert.Equal(t, map[string]string{
		"nombre": "is required",
		"correo": "must be a single email address",
		"tema":   "must be one of the configured options",
		"extra":  "is not a field of this form",
	}, map[string]string(errs))
}

func TestFormValidateRequiresAFieldOnlyWhenVisible(t *testing.T) {
	form, err := CompileForm(testRegistry(t), consultaFields(), consultaFields())
	require.NoError(t, err)

	_, errs := form.Validate(map[string]any{
		"nombre": "Ana", "correo": "ana@example.cl", "tema": "reclamo",
	}, nil)

	assert.Equal(t, map[string]string{"detalle_reclamo": "is required"}, map[string]string(errs))
}

func TestFormValidateDropsHiddenFields(t *testing.T) {
	form, err := CompileForm(testRegistry(t), consultaFields(), consultaFields())
	require.NoError(t, err)

	got, errs := form.Validate(map[string]any{
		"nombre": "Ana", "correo": "ana@example.cl", "tema": "general", "detalle_reclamo": "x",
	}, nil)

	assert.Empty(t, errs)
	assert.NotContains(t, got, "detalle_reclamo")
}

func TestFormValidateEvaluatesRulesAgainstEarlierCaseData(t *testing.T) {
	all := append(consultaFields(), FieldSpec{
		Key: "plazo", Type: "text", Label: "Plazo", Required: true, VisibleWhen: `tema == "reclamo"`,
	})
	respuesta := all[len(all)-1:]
	form, err := CompileForm(testRegistry(t), all, respuesta)
	require.NoError(t, err)

	_, errs := form.Validate(map[string]any{}, map[string]any{"tema": "reclamo"})
	assert.Equal(t, map[string]string{"plazo": "is required"}, map[string]string(errs))

	_, errs = form.Validate(map[string]any{}, map[string]any{"tema": "general"})
	assert.Empty(t, errs)
}
