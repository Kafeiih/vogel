package scaffold

import (
	"bytes"
	"fmt"
	"regexp"
)

var (
	// markerRe matches a marker line once surrounding whitespace is trimmed.
	markerRe = regexp.MustCompile(`^(//|#|--|<!--)\s*vogel:(component ([a-z]+)|only) (begin|end)\s*(-->)?$`)
	// markerLikeRe catches lines that look like a marker but do not match
	// markerRe, so a typo fails loudly instead of leaking into the output.
	markerLikeRe = regexp.MustCompile(`^(//|#|--|<!--)\s*vogel:(component|only)\b`)
)

// MarkerError reports an invalid marker. File is set by the caller that knows
// which file was being processed.
type MarkerError struct {
	File string
	Line int
	Msg  string
}

func (e *MarkerError) Error() string {
	if e.File == "" {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
}

type markerFrame struct {
	name string // empty for a vogel:only block
	line int
	keep bool
}

func (f markerFrame) label() string {
	if f.name == "" {
		return "vogel:only"
	}
	return "vogel:component " + f.name
}

// StripMarkers removes marker lines from content. A `vogel:component <name>`
// block is kept (without its markers) when selection[name] is true and
// deleted when it is false; a `vogel:only` block is always deleted. Names
// missing from selection are unknown components and fail. Blocks nest: a
// deleted outer block deletes everything inside it.
func StripMarkers(content []byte, selection map[string]bool) ([]byte, error) {
	var (
		out   bytes.Buffer
		stack []markerFrame
	)
	out.Grow(len(content))
	keeping := func() bool {
		for _, f := range stack {
			if !f.keep {
				return false
			}
		}
		return true
	}

	rest := content
	for lineNo := 1; len(rest) > 0; lineNo++ {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		trimmed := bytes.TrimSpace(line)
		m := markerRe.FindSubmatch(trimmed)
		if m == nil {
			if markerLikeRe.Match(trimmed) {
				return nil, &MarkerError{Line: lineNo, Msg: fmt.Sprintf("malformed marker %q", trimmed)}
			}
			if keeping() {
				out.Write(line)
			}
			continue
		}
		if err := checkMarkerSyntax(m); err != nil {
			return nil, &MarkerError{Line: lineNo, Msg: err.Error()}
		}

		frame := markerFrame{name: string(m[3]), line: lineNo}
		if frame.name != "" {
			selected, known := selection[frame.name]
			if !known {
				return nil, &MarkerError{Line: lineNo, Msg: fmt.Sprintf("unknown component %q", frame.name)}
			}
			frame.keep = selected
		}

		if string(m[4]) == "begin" {
			stack = append(stack, frame)
			continue
		}
		if len(stack) == 0 {
			return nil, &MarkerError{Line: lineNo, Msg: fmt.Sprintf("%s end without a matching begin", frame.label())}
		}
		top := stack[len(stack)-1]
		if top.name != frame.name {
			return nil, &MarkerError{Line: lineNo, Msg: fmt.Sprintf("%s end does not match %s begin at line %d", frame.label(), top.label(), top.line)}
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return nil, &MarkerError{Line: top.line, Msg: fmt.Sprintf("%s begin is never closed", top.label())}
	}
	return out.Bytes(), nil
}

// checkMarkerSyntax rejects an HTML-comment opener without its closer and a
// closer after a non-HTML opener.
func checkMarkerSyntax(m [][]byte) error {
	html := string(m[1]) == "<!--"
	closed := len(m[5]) > 0
	if html != closed {
		return fmt.Errorf("marker comment %q is not closed consistently", m[0])
	}
	return nil
}
