package scaffold

import (
	"errors"
	"strings"
	"testing"
)

func TestStripMarkers(t *testing.T) {
	selection := map[string]bool{"worker": true, "notification": false, "cerbos": false}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "selected go block keeps body and drops markers",
			in:   "a\n// vogel:component worker begin\nb\n// vogel:component worker end\nc\n",
			want: "a\nb\nc\n",
		},
		{
			name: "deselected yaml block is removed with markers",
			in:   "a\n  # vogel:component cerbos begin\n  b\n  # vogel:component cerbos end\nc\n",
			want: "a\nc\n",
		},
		{
			name: "sql and markdown syntaxes",
			in:   "-- vogel:component cerbos begin\nx\n-- vogel:component cerbos end\n<!-- vogel:component worker begin -->\ny\n<!-- vogel:component worker end -->\n",
			want: "y\n",
		},
		{
			name: "only block is always removed",
			in:   "a\n#vogel:only begin\nb\n#vogel:only end\nc\n",
			want: "a\nc\n",
		},
		{
			name: "nested deselected inside selected",
			in:   "// vogel:component worker begin\nw\n// vogel:component notification begin\nn\n// vogel:component notification end\nw2\n// vogel:component worker end\n",
			want: "w\nw2\n",
		},
		{
			name: "deselected outer removes selected inner",
			in:   "x\n// vogel:component cerbos begin\nc\n// vogel:component worker begin\nw\n// vogel:component worker end\n// vogel:component cerbos end\n",
			want: "x\n",
		},
		{
			name: "file without markers is unchanged",
			in:   "plain\ntext without trailing newline",
			want: "plain\ntext without trailing newline",
		},
		{
			name: "marker on last line without newline",
			in:   "a\n// vogel:component cerbos begin\nb\n// vogel:component cerbos end",
			want: "a\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StripMarkers([]byte(tt.in), selection)
			if err != nil {
				t.Fatalf("StripMarkers() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("StripMarkers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripMarkersErrors(t *testing.T) {
	selection := map[string]bool{"worker": true, "cerbos": false}

	tests := []struct {
		name     string
		in       string
		wantLine int
		wantMsg  string
	}{
		{
			name:     "end without begin",
			in:       "a\n// vogel:component worker end\n",
			wantLine: 2,
			wantMsg:  "without a matching begin",
		},
		{
			name:     "name mismatch",
			in:       "// vogel:component worker begin\n// vogel:component cerbos end\n",
			wantLine: 2,
			wantMsg:  "does not match",
		},
		{
			name:     "only closed as component",
			in:       "# vogel:only begin\n# vogel:component worker end\n",
			wantLine: 2,
			wantMsg:  "does not match",
		},
		{
			name:     "unclosed at EOF",
			in:       "x\n// vogel:component worker begin\ny\n",
			wantLine: 2,
			wantMsg:  "never closed",
		},
		{
			name:     "unknown component",
			in:       "// vogel:component billing begin\n// vogel:component billing end\n",
			wantLine: 1,
			wantMsg:  `unknown component "billing"`,
		},
		{
			name:     "malformed marker",
			in:       "# vogel:component Worker begin\n",
			wantLine: 1,
			wantMsg:  "malformed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := StripMarkers([]byte(tt.in), selection)
			var me *MarkerError
			if !errors.As(err, &me) {
				t.Fatalf("StripMarkers() error = %v, want *MarkerError", err)
			}
			if me.Line != tt.wantLine {
				t.Fatalf("line = %d, want %d (%v)", me.Line, tt.wantLine, err)
			}
			if !strings.Contains(me.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", me.Error(), tt.wantMsg)
			}
		})
	}
}

func TestMarkerErrorNamesFile(t *testing.T) {
	err := &MarkerError{File: "deploy/compose.yaml", Line: 7, Msg: "boom"}
	if got, want := err.Error(), "deploy/compose.yaml:7: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
