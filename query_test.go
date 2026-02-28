package simpleworkflow

import "testing"

func TestRewritePlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no placeholders", "SELECT 1", "SELECT 1"},
		{"single placeholder", "WHERE id = $1", "WHERE id = ?"},
		{"multiple placeholders", "WHERE id = $1 AND status = $2", "WHERE id = ? AND status = ?"},
		{"double digit placeholder", "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)", "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"},
		{"placeholder in string literal", "WHERE name = '$1' AND id = $1", "WHERE name = '$1' AND id = ?"},
		{"adjacent dollar not placeholder", "SELECT $0 FROM t", "SELECT $0 FROM t"},
		{"dollar at end of string", "SELECT $", "SELECT $"},
		{"dollar followed by non-digit", "SELECT $a FROM t", "SELECT $a FROM t"},
		{"empty string", "", ""},
		{"real query", `INSERT INTO workflow_run (id, type) VALUES ($1, $2) RETURNING id`, `INSERT INTO workflow_run (id, type) VALUES (?, ?) RETURNING id`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RewritePlaceholders(tt.input)
			if got != tt.want {
				t.Errorf("RewritePlaceholders(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
