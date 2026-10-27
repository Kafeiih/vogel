// Package openapitest holds test helpers that keep a hand-wired chi router
// and the Go types it serves in sync with an OpenAPI document. See package
// openapi for the pattern they support.
package openapitest

import (
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.yaml.in/yaml/v3"
)

type document struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Required   []string       `yaml:"required"`
			Properties map[string]any `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func parse(t testing.TB, spec []byte) (document, bool) {
	t.Helper()
	var doc document
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("parse OpenAPI spec: %v", err)
		return doc, false
	}
	return doc, true
}

// Option configures RoutesMatchSpec.
type Option func(*options)

type options struct {
	prefix string
	ignore []string
}

// Prefix is prepended to every spec path before comparing, for a router that
// mounts the contract under a base path (for example "/v1").
func Prefix(prefix string) Option {
	return func(o *options) { o.prefix = strings.TrimSuffix(prefix, "/") }
}

// Ignore lists routes the router serves outside the contract, as
// "METHOD /path" with the full router path (for example "GET /healthz"). An
// ignored route the router does not serve is reported, so the list cannot
// go stale.
func Ignore(routes ...string) Option {
	return func(o *options) { o.ignore = append(o.ignore, routes...) }
}

var methods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace,
}

// chiParam matches a chi parameter with a regexp ({id:[0-9]+}).
var chiParam = regexp.MustCompile(`\{([^}:]+):[^}]*\}`)

// routeKey normalizes "METHOD /path": chi parameter regexps are dropped and
// a trailing slash is ignored, so chi's "/audit/" matches the spec's "/audit".
func routeKey(method, path string) string {
	path = chiParam.ReplaceAllString(path, "{$1}")
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return method + " " + path
}

// RoutesMatchSpec fails t when an operation in spec is not registered on
// routes, or when routes serves a route spec does not describe. Routes that
// are registered by hand next to their guards drift from the contract
// silently; this is what catches it. Wildcard routes ("/*") must be listed
// with Ignore.
func RoutesMatchSpec(t testing.TB, spec []byte, routes chi.Routes, opts ...Option) {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	doc, ok := parse(t, spec)
	if !ok {
		return
	}

	inSpec := map[string]bool{}
	for path, item := range doc.Paths {
		for key := range item {
			if method := strings.ToUpper(key); slices.Contains(methods, method) {
				inSpec[routeKey(method, o.prefix+path)] = true
			}
		}
	}
	ignored := map[string]bool{}
	for _, route := range o.ignore {
		method, path, _ := strings.Cut(route, " ")
		ignored[routeKey(method, path)] = false
	}

	served := map[string]bool{}
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := routeKey(method, route)
		if _, ok := ignored[key]; ok {
			ignored[key] = true
		} else {
			served[key] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk router: %v", err)
		return
	}

	for _, key := range sortedKeys(inSpec) {
		if !served[key] {
			t.Errorf("%s is in the OpenAPI spec but not registered on the router", key)
		}
	}
	for _, key := range sortedKeys(served) {
		if !inSpec[key] {
			t.Errorf("%s is served but not described in the OpenAPI spec", key)
		}
	}
	for _, key := range sortedKeys(ignored) {
		if !ignored[key] {
			t.Errorf("%s is ignored but not served by the router", key)
		}
	}
}

// SchemaMatchesType fails t unless the schema named name in spec describes
// the JSON encoding of T: the same properties as T's JSON fields, and
// required exactly where the field's tag has no omitempty. Use it for schemas
// whose Go type is not generated, such as vogel's error envelopes or a DTO
// mapped with x-go-type.
func SchemaMatchesType[T any](t testing.TB, spec []byte, name string) {
	t.Helper()
	doc, ok := parse(t, spec)
	if !ok {
		return
	}
	schema, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("schema %s missing from the OpenAPI spec", name)
		return
	}

	wantProps, wantRequired := jsonFields(reflect.TypeFor[T]())
	gotProps := make([]string, 0, len(schema.Properties))
	for prop := range schema.Properties {
		gotProps = append(gotProps, prop)
	}
	gotRequired := slices.Clone(schema.Required)
	for _, s := range [][]string{wantProps, wantRequired, gotProps, gotRequired} {
		slices.Sort(s)
	}

	if !slices.Equal(gotProps, wantProps) {
		t.Errorf("schema %s properties = %v, want the Go type's %v", name, gotProps, wantProps)
	}
	if !slices.Equal(gotRequired, wantRequired) {
		t.Errorf("schema %s required = %v, want the Go type's %v", name, gotRequired, wantRequired)
	}
}

// jsonFields returns the JSON property names of struct type typ and those
// that are required (no omitempty). An embedded struct without a JSON name
// contributes its own fields, as encoding/json flattens it.
func jsonFields(typ reflect.Type) (props, required []string) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		prop, opts, _ := strings.Cut(tag, ",")
		if field.Anonymous && prop == "" {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				p, r := jsonFields(embedded)
				props, required = append(props, p...), append(required, r...)
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		if prop == "" {
			prop = field.Name
		}
		props = append(props, prop)
		if !slices.Contains(strings.Split(opts, ","), "omitempty") {
			required = append(required, prop)
		}
	}
	return props, required
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
