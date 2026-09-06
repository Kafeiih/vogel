package stringutil

import "testing"

func TestNormalize_Basic(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercase", "HELLO", "hello"},
		{"trim spaces", "  hello  ", "hello"},
		{"collapse spaces", "hello   world", "hello world"},
		{"strip accents", "café", "cafe"},
		{"strip tilde", "niño", "nino"},
		{"combined", "  María  José  ", "maria jose"},
		{"empty", "", ""},
		{"only spaces", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.input)
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalize_SpanishNames(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"common name", "José García", "jose garcia"},
		{"uppercase accented", "ÁNGEL LÓPEZ", "angel lopez"},
		{"dieresis", "Agüero", "aguero"},
		{"multiple accents", "Ñoño Pérez Muñoz", "nono perez munoz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.input)
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalize_Tabs_And_Newlines(t *testing.T) {
	got := Normalize("hello\t\tworld\nfoo")
	want := "hello world foo"
	if got != want {
		t.Errorf("Normalize(tabs+newlines) = %q, want %q", got, want)
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	input := "María José García"
	first := Normalize(input)
	second := Normalize(first)
	if first != second {
		t.Errorf("not idempotent: first=%q, second=%q", first, second)
	}
}
