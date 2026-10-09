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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyPresignGetOptions(AsAttachment(tt.filename))
			if got.ContentDisposition != tt.want {
				t.Errorf("ContentDisposition = %q, want %q", got.ContentDisposition, tt.want)
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
