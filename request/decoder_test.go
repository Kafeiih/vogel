package request_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/request"
)

// decodeCase triggers one decode error branch; limit 0 means request.JSON's
// default limit.
type decodeCase struct {
	name  string
	body  string
	limit int64
}

var decodeBranches = []decodeCase{
	{name: "malformed", body: `{this is not json}`},
	{name: "wrong type", body: `{"name":1}`},
	{name: "too large", body: `{"name":"` + strings.Repeat("x", 64) + `"}`, limit: 16},
	{name: "empty", body: ``},
	{name: "unknown field", body: `{"bogus":1}`},
	{name: "generic", body: `{"name":"a"`},
}

func runDecode(t *testing.T, d *request.Decoder, c decodeCase) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(c.body))
	w := httptest.NewRecorder()
	var dst testPayload
	if c.limit > 0 {
		require.Error(t, d.JSONWithLimit(w, r, &dst, c.limit))
	} else {
		require.Error(t, d.JSON(w, r, &dst))
	}
	return w
}

// TestDecoder_UninitializedFallsBackToDefaults guards against a Decoder built
// without New — a zero value or a nil pointer. It must behave exactly like
// the package-level functions instead of panicking on a nil message func.
func TestDecoder_UninitializedFallsBackToDefaults(t *testing.T) {
	var nilDecoder *request.Decoder
	decoders := map[string]*request.Decoder{
		"zero value":  {},
		"nil pointer": nilDecoder,
	}

	for name, d := range decoders {
		t.Run(name, func(t *testing.T) {
			for _, c := range decodeBranches {
				t.Run(c.name, func(t *testing.T) {
					got := runDecode(t, d, c)
					want := runDecode(t, request.New(), c)
					assert.Equal(t, want.Code, got.Code)
					assert.Equal(t, want.Body.String(), got.Body.String())
				})
			}

			v := d.NewValidator()
			v.UUIDParam(httptest.NewRequest(http.MethodGet, "/", nil), "id")
			assert.Equal(t, "id is required", v.Errors()["id"])
		})
	}
}

// TestWithMessages_OverridesEveryField proves the merge in WithMessages copies
// each of the 18 fields to its own slot: every message is overridden with a
// marker naming the field, and each branch must surface its own marker.
func TestWithMessages_OverridesEveryField(t *testing.T) {
	mark := func(name string) func(string) string {
		return func(field string) string { return name + ":" + field }
	}
	d := request.New(request.WithMessages(request.Messages{
		MalformedJSON:      func(int64) string { return "MalformedJSON" },
		WrongType:          func(string, string) string { return "WrongType" },
		BodyTooLarge:       func(float64) string { return "BodyTooLarge" },
		EmptyBody:          "EmptyBody",
		UnknownField:       func(string) string { return "UnknownField" },
		InvalidBody:        "InvalidBody",
		Required:           mark("Required"),
		InvalidUUID:        mark("InvalidUUID"),
		NotInteger:         mark("NotInteger"),
		Negative:           mark("Negative"),
		InvalidRFC3339:     mark("InvalidRFC3339"),
		InvalidISODate:     mark("InvalidISODate"),
		InvalidInteger:     mark("InvalidInteger"),
		NotAllowed:         mark("NotAllowed"),
		InvalidPublicID:    mark("InvalidPublicID"),
		InvalidIntegerList: mark("InvalidIntegerList"),
		InvalidBoolean:     mark("InvalidBoolean"),
		InvalidDecimal:     mark("InvalidDecimal"),
	}))

	wantDecode := []string{"MalformedJSON", "WrongType", "BodyTooLarge", "EmptyBody", "UnknownField", "InvalidBody"}
	for i, c := range decodeBranches {
		t.Run("decode "+c.name, func(t *testing.T) {
			var body errorEnvelope
			require.NoError(t, json.Unmarshal(runDecode(t, d, c).Body.Bytes(), &body))
			assert.Equal(t, wantDecode[i], body.Message)
		})
	}

	withParam := func(name, value string) *http.Request {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add(name, value)
		return requestWithRouteContext(httptest.NewRequest(http.MethodGet, "/", nil), rctx)
	}
	query := func(q string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/?"+q, nil)
	}
	validatorCases := []struct {
		want string
		run  func(v *request.Validator)
	}{
		{"Required:f", func(v *request.Validator) { v.UUIDParam(query(""), "f") }},
		{"InvalidUUID:f", func(v *request.Validator) { v.UUIDQuery(query("f=x"), "f") }},
		{"NotInteger:f", func(v *request.Validator) { v.IntQuery(query("f=x"), "f", 0) }},
		{"Negative:f", func(v *request.Validator) { v.IntQuery(query("f=-1"), "f", 0) }},
		{"InvalidRFC3339:f", func(v *request.Validator) { v.TimeQuery(query("f=x"), "f") }},
		{"InvalidISODate:f", func(v *request.Validator) { v.DateQuery(query("f=x"), "f") }},
		{"InvalidInteger:f", func(v *request.Validator) { v.Int64Param(withParam("f", "x"), "f") }},
		{"NotAllowed:f", func(v *request.Validator) { v.Enum("f", "x", []string{"y"}) }},
		{"InvalidPublicID:f", func(v *request.Validator) { v.PublicIDParam(withParam("f", "x"), "f") }},
		{"InvalidIntegerList:f", func(v *request.Validator) { v.Int64sQuery(query("f=1,x"), "f") }},
		{"InvalidInteger:f", func(v *request.Validator) { v.Int64Query(query("f=x"), "f") }},
		{"InvalidBoolean:f", func(v *request.Validator) { v.BoolQuery(query("f=x"), "f") }},
	}
	for _, c := range validatorCases {
		t.Run("validator "+c.want, func(t *testing.T) {
			v := d.NewValidator()
			c.run(v)
			assert.Equal(t, c.want, v.Errors()["f"])
		})
	}

	t.Run("Messages accessor surfaces InvalidDecimal", func(t *testing.T) {
		v := d.NewValidator()
		assert.Equal(t, "InvalidDecimal:amount", v.Messages().InvalidDecimal("amount"))
	})
}
