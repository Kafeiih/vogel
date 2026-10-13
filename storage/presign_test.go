package storage

import "testing"

func TestAsAttachment(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			name:     "plain name is quoted",
			filename: "report.pdf",
			want:     `attachment; filename="report.pdf"`,
		},
		{
			name:     "double quotes are stripped so the header cannot be closed early",
			filename: `re"port".pdf`,
			want:     `attachment; filename="report.pdf"`,
		},
		{
			name:     "CR and LF are stripped so the header cannot be split",
			filename: "report\r\nX-Injected: 1.pdf",
			want:     `attachment; filename="reportX-Injected: 1.pdf"`,
		},
		{
			name:     "backslash is stripped so the quoted-string cannot be escaped",
			filename: `re\port.pdf`,
			want:     `attachment; filename="report.pdf"`,
		},
		{
			name:     "surrounding whitespace is trimmed",
			filename: "  report.pdf \t",
			want:     `attachment; filename="report.pdf"`,
		},
		{
			name:     "empty name falls back to a bare attachment",
			filename: "",
			want:     "attachment",
		},
		{
			name:     "name made only of stripped characters falls back to a bare attachment",
			filename: "\"\r\n\\",
			want:     "attachment",
		},
		{
			name:     "control characters are stripped",
			filename: "re\x00po\trt\x1b\x7f.pdf",
			want:     `attachment; filename="report.pdf"`,
		},
		{
			name:     "name made only of control characters falls back to a bare attachment",
			filename: "\x00\x01\t\x7f\u0085",
			want:     "attachment",
		},
		{
			name:     "non-ASCII name gets an ASCII fallback and an RFC 5987 filename*",
			filename: "informe-año.pdf",
			want:     `attachment; filename="informe-a_o.pdf"; filename*=UTF-8''informe-a%C3%B1o.pdf`,
		},
		{
			name:     "CR/LF injection in a non-ASCII name stays on one header line",
			filename: "año\r\nSet-Cookie: x=1 .pdf",
			want:     `attachment; filename="a_oSet-Cookie: x=1.pdf"; filename*=UTF-8''a%C3%B1oSet-Cookie%3A%20x%3D1.pdf`,
		},
		{
			name:     "C1 controls and bidi overrides are stripped from both forms",
			filename: "fac\u0085tura\u202egpj.exe ñ",
			want:     `attachment; filename="facturagpj.exe _"; filename*=UTF-8''facturagpj.exe%20%C3%B1`,
		},
		{
			name:     "fully non-ASCII name keeps a placeholder fallback",
			filename: "報告",
			want:     `attachment; filename="__"; filename*=UTF-8''%E5%A0%B1%E5%91%8A`,
		},
		{
			name:     "RFC 5987 attr-chars are not percent-encoded",
			filename: "a!#$&+-.^_`|~1ñ%",
			want:     "attachment; filename=\"a!#$&+-.^_`|~1_%\"; filename*=UTF-8''a!#$&+-.^_`|~1%C3%B1%25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyPresignGetOptions(AsAttachment(tt.filename))
			if got.ContentDisposition != tt.want {
				t.Errorf("ContentDisposition = %q, want %q", got.ContentDisposition, tt.want)
			}
			for _, r := range got.ContentDisposition {
				if r < 0x20 || r > 0x7e {
					t.Fatalf("ContentDisposition %q contains rune %U outside printable ASCII", got.ContentDisposition, r)
				}
			}
		})
	}
}

func TestApplyPresignGetOptions(t *testing.T) {
	tests := []struct {
		name string
		opts []PresignGetOption
		want PresignGetOptions
	}{
		{
			name: "no options default to attachment and no content type override",
			opts: nil,
			want: PresignGetOptions{ContentDisposition: DefaultContentDisposition},
		},
		{
			name: "WithContentDisposition overrides the default",
			opts: []PresignGetOption{WithContentDisposition("inline")},
			want: PresignGetOptions{ContentDisposition: "inline"},
		},
		{
			name: "WithContentDisposition with an empty value keeps the secure default",
			opts: []PresignGetOption{WithContentDisposition("")},
			want: PresignGetOptions{ContentDisposition: DefaultContentDisposition},
		},
		{
			name: "WithResponseContentType is kept alongside the default disposition",
			opts: []PresignGetOption{WithResponseContentType("application/pdf")},
			want: PresignGetOptions{ContentDisposition: DefaultContentDisposition, ContentType: "application/pdf"},
		},
		{
			name: "last disposition option wins",
			opts: []PresignGetOption{AsAttachment("a.pdf"), WithContentDisposition("inline")},
			want: PresignGetOptions{ContentDisposition: "inline"},
		},
		{
			name: "nil option is ignored",
			opts: []PresignGetOption{nil, WithResponseContentType("text/plain")},
			want: PresignGetOptions{ContentDisposition: DefaultContentDisposition, ContentType: "text/plain"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyPresignGetOptions(tt.opts...)
			if got != tt.want {
				t.Errorf("ApplyPresignGetOptions() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
