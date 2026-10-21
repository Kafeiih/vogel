package process

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/workflow"
)

func loadConsultaSpec(t *testing.T) ProcessSpec {
	t.Helper()
	raw, err := os.ReadFile("../../infrastructure/bpms/processes/consulta.json")
	require.NoError(t, err)
	spec, err := ParseProcessSpec(raw)
	require.NoError(t, err)
	return spec
}

func TestParseProcessSpecRejectsUnknownKeys(t *testing.T) {
	_, err := ParseProcessSpec([]byte(`{"name": "x", "nodes": [], "transitions": [], "owner": "y"}`))
	assert.Error(t, err)
}

func TestParseProcessSpecRejectsAVersion(t *testing.T) {
	_, err := ParseProcessSpec([]byte(`{"name": "x", "version": 3, "nodes": [], "transitions": []}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestCompileProcessBuildsTheConsultaDefinition(t *testing.T) {
	p, err := CompileProcess(testRegistry(t), loadConsultaSpec(t), 2)
	require.NoError(t, err)

	assert.Equal(t, "consulta@2", p.Definition.Key())
	respuesta, ok := p.Definition.Node("respuesta")
	require.True(t, ok)
	assert.Equal(t, workflow.ByPositionInUnit("encargado", "atencion"), respuesta.Eligible)
	assert.Contains(t, p.Forms, "ingreso")
	assert.Contains(t, p.Forms, "respuesta")
}

func TestCompileProcessRejectsAnInvalidGraph(t *testing.T) {
	spec := loadConsultaSpec(t)
	spec.Transitions = append(spec.Transitions, TransitionSpec{From: "respuesta", To: "nowhere", Action: "derivar"})

	_, err := CompileProcess(testRegistry(t), spec, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidDefinition)
}

func TestCompileProcessRejectsAFormOnATerminalNode(t *testing.T) {
	spec := loadConsultaSpec(t)
	for i := range spec.Nodes {
		if spec.Nodes[i].Terminal {
			spec.Nodes[i].Form = []FieldSpec{{Key: "x", Type: "text"}}
		}
	}

	_, err := CompileProcess(testRegistry(t), spec, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}

func TestCompileProcessRejectsAProfileOnTheStartNode(t *testing.T) {
	spec := loadConsultaSpec(t)
	spec.Nodes[0].Profile = &ProfileSpec{Position: "encargado", Unit: "atencion"}

	_, err := CompileProcess(testRegistry(t), spec, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start node")
}

func TestCompileProcessRequiresAProfileUnit(t *testing.T) {
	spec := loadConsultaSpec(t)
	spec.Nodes[1].Profile.Unit = ""

	_, err := CompileProcess(testRegistry(t), spec, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unit")
}

func TestCompileProcessRejectsAFieldDeclaredOnTwoNodes(t *testing.T) {
	spec := loadConsultaSpec(t)
	spec.Nodes[1].Form = append(spec.Nodes[1].Form, spec.Nodes[0].Form[0])

	_, err := CompileProcess(testRegistry(t), spec, 1)
	assert.Error(t, err)
}

func TestCompileProcessRejectsABadRule(t *testing.T) {
	spec := loadConsultaSpec(t)
	spec.Nodes[0].Form[0].VisibleWhen = `missing == "x"`

	_, err := CompileProcess(testRegistry(t), spec, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "visibleWhen")
}

func TestProcessSpecRoundTrips(t *testing.T) {
	spec := loadConsultaSpec(t)
	raw, err := json.Marshal(spec)
	require.NoError(t, err)

	// Stored specs are compact JSON, so compare encodings rather than the
	// raw config bytes the author wrote.
	again, err := ParseProcessSpec(raw)
	require.NoError(t, err)
	rawAgain, err := json.Marshal(again)
	require.NoError(t, err)
	assert.JSONEq(t, string(raw), string(rawAgain))
	assert.Equal(t, spec.Name, again.Name)
	assert.Len(t, again.Nodes, len(spec.Nodes))
}

func TestProcessValidateNode(t *testing.T) {
	p, err := CompileProcess(testRegistry(t), loadConsultaSpec(t), 1)
	require.NoError(t, err)

	clean, err := p.ValidateNode("respuesta", map[string]any{"respuesta": "listo"}, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"respuesta": "listo"}, clean)

	_, err = p.ValidateNode("respuesta", map[string]any{}, nil)
	var formErr *FormError
	require.ErrorAs(t, err, &formErr)
	assert.Equal(t, "is required", formErr.Fields["respuesta"])

	clean, err = p.ValidateNode("cerrada", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, clean)

	_, err = p.ValidateNode("cerrada", map[string]any{"x": "y"}, nil)
	require.ErrorAs(t, err, &formErr)
	assert.Equal(t, "is not a field of this form", formErr.Fields["x"])
}

func TestProcessProfiledNodes(t *testing.T) {
	p, err := CompileProcess(testRegistry(t), loadConsultaSpec(t), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"respuesta"}, p.ProfiledNodes())
}
