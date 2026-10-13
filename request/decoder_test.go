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
	// contentType replaces the application/json header jsonRequest sets;
	// "" keeps it.
	contentType string
}

var decodeBranches = []decodeCase{
	{name: "malformed", body: `{this is not json}`},
	{name: "wrong type", body: `{"name":1}`},
	{name: "too large", body: `{"name":"` + strings.Repeat("x", 64) + `"}`, limit: 16},
	{name: "empty", body: ``},
	{name: "unknown field", body: `{"bogus":1}`},
	{name: "generic", body: `{"name":"a"`},
	{name: "unsupported media type", body: `{"name":"a"}`, contentType: "text/plain"},
	{name: "trailing data", body: `{"name":"a"} {"name":"b"}`},
}

func runDecode(t *testing.T, d *request.Decoder, c decodeCase) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(c.body)
	if c.contentType != "" {
		r.Header.Set("Content-Type", c.contentType)
	}
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
// each of the 20 fields to its own slot: every message is overridden with a
// marker naming the field, and each branch must surface its own marker.
func TestWithMessages_OverridesEveryField(t *testing.T) {
	mark := func(name string) func(string) string {
		return func(field string) string { return name + ":" + field }
	}
	d := request.New(request.WithMessages(request.Messages{
		MalformedJSON:        func(int64) string { return "MalformedJSON" },
		WrongType:            func(string, string) string { return "WrongType" },
		BodyTooLarge:         func(float64) string { return "BodyTooLarge" },
		EmptyBody:            "EmptyBody",
		UnknownField:         func(string) string { return "UnknownField" },
		InvalidBody:          "InvalidBody",
		UnsupportedMediaType: "UnsupportedMediaType",
		TrailingData:         "TrailingData",
		Required:             mark("Required"),
		InvalidUUID:          mark("InvalidUUID"),
		NotInteger:           mark("NotInteger"),
		Negative:             mark("Negative"),
		InvalidRFC3339:       mark("InvalidRFC3339"),
		InvalidISODate:       mark("InvalidISODate"),
		InvalidInteger:       mark("InvalidInteger"),
		NotAllowed:           mark("NotAllowed"),
		InvalidPublicID:      mark("InvalidPublicID"),
		InvalidIntegerList:   mark("InvalidIntegerList"),
		InvalidBoolean:       mark("InvalidBoolean"),
		InvalidDecimal:       mark("InvalidDecimal"),
	}))

	wantDecode := []string{"MalformedJSON", "WrongType", "BodyTooLarge", "EmptyBody", "UnknownField", "InvalidBody", "UnsupportedMediaType", "TrailingData"}
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

// TestAllowAnyContentType covers the opt-out for consumers whose legacy
// clients never send a JSON Content-Type: the media-type gate is off for
// that Decoder only, and every other rule — including the trailing-data
// rejection — still applies.
func TestAllowAnyContentType(t *testing.T) {
	d := request.New(request.AllowAnyContentType())

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "missing header decodes", body: `{"name":"hello"}`, wantStatus: http.StatusOK},
		{name: "text/plain decodes", contentType: "text/plain", body: `{"name":"hello"}`, wantStatus: http.StatusOK},
		{name: "application/json still decodes", contentType: "application/json", body: `{"name":"hello"}`, wantStatus: http.StatusOK},
		{name: "trailing data is still rejected", body: `{"name":"hello"}garbage`, wantStatus: http.StatusBadRequest},
		{name: "malformed body is still rejected", contentType: "text/plain", body: `{nope}`, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()

			var dst testPayload
			err := d.JSON(w, r, &dst)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, "hello", dst.Name)
				return
			}
			require.Error(t, err)
		})
	}

	t.Run("JSONOptional with no header and no body is still a no-op", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		w := httptest.NewRecorder()

		dst := testPayload{Name: "preset"}
		require.NoError(t, d.JSONOptional(w, r, &dst))
		assert.Equal(t, "preset", dst.Name)
		assert.Equal(t, 0, w.Body.Len())
	})

	t.Run("package-level functions keep the strict default", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"hello"}`))
		w := httptest.NewRecorder()

		var dst testPayload
		require.Error(t, request.JSON(w, r, &dst))
		assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	})
}

// TestAllowAnyContentType_WithMessages pins that the two options compose in
// either order: the gate is off and the custom messages are used.
func TestAllowAnyContentType_WithMessages(t *testing.T) {
	msgs := request.Messages{
		UnsupportedMediaType: "custom 415",
		TrailingData:         "custom trailing",
		EmptyBody:            "custom empty",
	}
	orders := map[string][]request.Option{
		"AllowAnyContentType first": {request.AllowAnyContentType(), request.WithMessages(msgs)},
		"WithMessages first":        {request.WithMessages(msgs), request.AllowAnyContentType()},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			d := request.New(opts...)

			decode := func(contentType, body string) (*httptest.ResponseRecorder, testPayload, error) {
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				if contentType != "" {
					r.Header.Set("Content-Type", contentType)
				}
				w := httptest.NewRecorder()
				var dst testPayload
				err := d.JSON(w, r, &dst)
				return w, dst, err
			}

			w, dst, err := decode("text/plain", `{"name":"hello"}`)
			require.NoError(t, err, "the gate is disabled")
			assert.Equal(t, "hello", dst.Name)
			assert.Equal(t, 0, w.Body.Len())

			for _, c := range []struct{ contentType, body, want string }{
				{"text/plain", `{"name":"hello"}garbage`, "custom trailing"},
				{"", ``, "custom empty"},
			} {
				w, _, err := decode(c.contentType, c.body)
				require.Error(t, err)
				assert.Equal(t, http.StatusBadRequest, w.Code)
				var body errorEnvelope
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, c.want, body.Message)
			}
		})
	}
}
