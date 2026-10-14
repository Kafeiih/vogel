package postgres

import "testing"

// CheckTLS applies NewPool's RequireTLS rule to a bare DSN, for callers that
// connect without NewPool (e.g. a migration tool handed the DSN string).
func TestCheckTLS(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		wantErr bool
	}{
		{name: "verify-full passes", dsn: "postgres://u:p@db.example.com:5432/app?sslmode=verify-full"},
		{name: "disable fails", dsn: "postgres://u:p@db.example.com:5432/app?sslmode=disable", wantErr: true},
		{name: "require without root cert fails", dsn: "postgres://u:p@db.example.com:5432/app?sslmode=require", wantErr: true},
		{name: "absent sslmode fails", dsn: "postgres://u:p@db.example.com:5432/app", wantErr: true},
		{name: "unparsable DSN fails", dsn: "postgres://u:p@db.example.com:notaport/app", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckTLS(tt.dsn)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckTLS(%q) error = %v, wantErr %v", tt.dsn, err, tt.wantErr)
			}
		})
	}
}
