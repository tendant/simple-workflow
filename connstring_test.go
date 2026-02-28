package simpleworkflow

import (
	"testing"
)

func TestDetectDialect(t *testing.T) {
	tests := []struct {
		name       string
		connString string
		wantDriver string
		wantDSN    string
		wantErr    bool
	}{
		{
			name:       "sqlite memory",
			connString: "sqlite://:memory:",
			wantDriver: "sqlite",
			wantDSN:    ":memory:",
		},
		{
			name:       "sqlite3 scheme",
			connString: "sqlite3://:memory:",
			wantDriver: "sqlite",
			wantDSN:    ":memory:",
		},
		{
			name:       "sqlite file path",
			connString: "sqlite:///tmp/test.db",
			wantDriver: "sqlite",
			wantDSN:    "/tmp/test.db",
		},
		{
			name:       "sqlite relative path",
			connString: "sqlite://test.db",
			wantDriver: "sqlite",
			wantDSN:    "test.db",
		},
		{
			name:       "sqlite with query params",
			connString: "sqlite:///tmp/test.db?_busy_timeout=5000",
			wantDriver: "sqlite",
			wantDSN:    "/tmp/test.db?_busy_timeout=5000",
		},
		{
			name:       "postgres scheme",
			connString: "postgres://user:pass@localhost/db",
			wantDriver: "postgres",
		},
		{
			name:       "postgresql scheme",
			connString: "postgresql://user:pass@localhost/db",
			wantDriver: "postgres",
		},
		{
			name:       "key value format defaults to postgres",
			connString: "host=localhost user=postgres dbname=test",
			wantDriver: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialect, dsn, err := DetectDialect(tt.connString)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DetectDialect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if dialect.DriverName() != tt.wantDriver {
				t.Errorf("DriverName() = %q, want %q", dialect.DriverName(), tt.wantDriver)
			}
			if tt.wantDSN != "" && dsn != tt.wantDSN {
				t.Errorf("DSN = %q, want %q", dsn, tt.wantDSN)
			}
		})
	}
}

func TestParseConnString(t *testing.T) {
	tests := []struct {
		name       string
		connString string
		schema     string
		wantSchema string
		wantErr    bool
	}{
		{
			name:       "URL with schema param",
			connString: "postgres://user:pass@localhost/db?schema=custom",
			schema:     "default",
			wantSchema: "custom",
		},
		{
			name:       "URL with search_path",
			connString: "postgres://user:pass@localhost/db?search_path=existing",
			schema:     "default",
			wantSchema: "existing",
		},
		{
			name:       "URL with no schema uses default",
			connString: "postgres://user:pass@localhost/db",
			schema:     "workflow",
			wantSchema: "workflow",
		},
		{
			name:       "key value with search_path",
			connString: "host=localhost search_path=myschema dbname=test",
			schema:     "default",
			wantSchema: "myschema",
		},
		{
			name:       "key value without search_path adds default",
			connString: "host=localhost dbname=test",
			schema:     "workflow",
			wantSchema: "workflow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gotSchema, err := ParseConnString(tt.connString, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseConnString() error = %v, wantErr %v", err, tt.wantErr)
			}
			if gotSchema != tt.wantSchema {
				t.Errorf("schema = %q, want %q", gotSchema, tt.wantSchema)
			}
		})
	}
}
