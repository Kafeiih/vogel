package audit

import (
	"context"
	"fmt"
	"testing"

	"github.com/kafeiih/vogel/pgxtx"
)

// discardRepository drops every entry, so a benchmark loop does not keep
// b.N entries alive the way fakeRepository would. Record only calls Create;
// the embedded nil Repository covers the rest of the interface.
type discardRepository struct{ Repository }

func (discardRepository) Create(context.Context, pgxtx.DBTX, *Entry) error { return nil }

// typedObject is a map type the walk does not accept as-is: normalizeValue
// sends it through encoding/json, like any value without a plain JSON form.
type typedObject map[string]any

// benchPayload builds a payload nested depth levels deep. Every level is a
// map with width keys: width-1 string leaves and, except at the deepest
// level, one "child" key holding the next level. The deepest level also
// holds the single "marker" key the one-key hook replaces. No key is on the
// default denylist, so every value is walked.
func benchPayload(depth, width int) map[string]any {
	var child map[string]any
	for level := depth; level >= 1; level-- {
		m := make(map[string]any, width)
		for i := range width - 1 {
			m[fmt.Sprintf("field_%d", i)] = "value"
		}
		if child != nil {
			m["child"] = child
		} else {
			m["marker"] = "value"
		}
		child = m
	}
	return child
}

// BenchmarkRecord_Redaction measures Record's redaction pass over payloads of
// growing width and depth, with and without WithRedactor hooks. Record walks
// every hook result again with the denylist, and a result with no plain JSON
// form (a named map type, a struct) is first normalized through
// encoding/json; the hypothesis it measures is that this re-walk can cost
// roughly payload size times nesting depth. Measured: a hook returning plain
// map[string]any values adds no cost, while one returning typed values grows
// with about the square of the depth.
func BenchmarkRecord_Redaction(b *testing.B) {
	hooks := []struct {
		name  string
		ropts []RecorderOption
	}{
		{"default_denylist_no_hook", nil},
		{"hook_declines_every_key", []RecorderOption{WithRedactor(func(string, any) (any, bool) {
			return nil, false
		})}},
		{"hook_returns_input_every_key", []RecorderOption{WithRedactor(func(_ string, v any) (any, bool) {
			return v, true
		})}},
		{"hook_replaces_one_key", []RecorderOption{WithRedactor(func(k string, _ any) (any, bool) {
			if k == "marker" {
				return "masked", true
			}
			return nil, false
		})}},
		// The costliest shape of the same path: every map the hook returns
		// is a typed value, so it must be re-encoded through
		// encoding/json before it is walked again, at every level.
		{"hook_returns_typed_copy_every_key", []RecorderOption{WithRedactor(func(_ string, v any) (any, bool) {
			if m, ok := v.(map[string]any); ok {
				return typedObject(m), true
			}
			return v, true
		})}},
	}

	for _, h := range hooks {
		for _, depth := range []int{1, 8, 32, 128} {
			for _, width := range []int{10, 100} {
				name := fmt.Sprintf("%s/depth=%d/width=%d", h.name, depth, width)
				b.Run(name, func(b *testing.B) {
					payload := benchPayload(depth, width)
					r := NewRecorder(discardRepository{}, h.ropts...)
					ctx := context.Background()
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						if err := r.Record(ctx, nil, SourceHTTP, ActionUpdate, "widget", "w-1", WithInputPayload(payload)); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
