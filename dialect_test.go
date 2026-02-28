package simpleworkflow

import (
	"strings"
	"testing"
)

func TestPostgresDialect(t *testing.T) {
	d := &PostgresDialect{}

	t.Run("DriverName", func(t *testing.T) {
		if got := d.DriverName(); got != "postgres" {
			t.Errorf("DriverName() = %q, want %q", got, "postgres")
		}
	})

	t.Run("Placeholder", func(t *testing.T) {
		tests := []struct{ n int; want string }{
			{1, "$1"}, {2, "$2"}, {10, "$10"},
		}
		for _, tt := range tests {
			if got := d.Placeholder(tt.n); got != tt.want {
				t.Errorf("Placeholder(%d) = %q, want %q", tt.n, got, tt.want)
			}
		}
	})

	t.Run("Now", func(t *testing.T) {
		if got := d.Now(); got != "NOW()" {
			t.Errorf("Now() = %q, want %q", got, "NOW()")
		}
	})

	t.Run("TimestampAfterNow", func(t *testing.T) {
		got := d.TimestampAfterNow(30)
		if got != "NOW() + interval '30 seconds'" {
			t.Errorf("TimestampAfterNow(30) = %q", got)
		}
	})

	t.Run("IntervalParam", func(t *testing.T) {
		frag, arg := d.IntervalParam(2, 30)
		if frag != "$2::interval" {
			t.Errorf("IntervalParam fragment = %q, want %q", frag, "$2::interval")
		}
		if arg != "30 seconds" {
			t.Errorf("IntervalParam arg = %q, want %q", arg, "30 seconds")
		}
	})

	t.Run("ClaimRunQuery contains FOR UPDATE SKIP LOCKED", func(t *testing.T) {
		q := d.ClaimRunQuery("", 30)
		if !strings.Contains(q, "FOR UPDATE SKIP LOCKED") {
			t.Errorf("ClaimRunQuery missing FOR UPDATE SKIP LOCKED")
		}
		if !strings.Contains(q, "$1") {
			t.Errorf("ClaimRunQuery missing $1 placeholder")
		}
		if !strings.Contains(q, "$2::interval") {
			t.Errorf("ClaimRunQuery missing $2::interval")
		}
	})

	t.Run("ClaimRunQuery with type condition", func(t *testing.T) {
		q := d.ClaimRunQuery("AND type LIKE $3", 30)
		if !strings.Contains(q, "AND type LIKE $3") {
			t.Errorf("ClaimRunQuery missing type condition")
		}
	})

	t.Run("ClaimSchedulesQuery contains FOR UPDATE SKIP LOCKED", func(t *testing.T) {
		q := d.ClaimSchedulesQuery()
		if !strings.Contains(q, "FOR UPDATE SKIP LOCKED") {
			t.Errorf("ClaimSchedulesQuery missing FOR UPDATE SKIP LOCKED")
		}
		if !strings.Contains(q, "enabled = true") {
			t.Errorf("ClaimSchedulesQuery missing enabled = true")
		}
	})

	t.Run("MigrateSQL contains postgres-specific syntax", func(t *testing.T) {
		sql := d.MigrateSQL()
		if !strings.Contains(sql, "UUID PRIMARY KEY") {
			t.Errorf("MigrateSQL missing UUID PRIMARY KEY")
		}
		if !strings.Contains(sql, "JSONB") {
			t.Errorf("MigrateSQL missing JSONB")
		}
		if !strings.Contains(sql, "TIMESTAMPTZ") {
			t.Errorf("MigrateSQL missing TIMESTAMPTZ")
		}
	})
}

func TestSQLiteDialect(t *testing.T) {
	d := &SQLiteDialect{}

	t.Run("DriverName", func(t *testing.T) {
		if got := d.DriverName(); got != "sqlite" {
			t.Errorf("DriverName() = %q, want %q", got, "sqlite")
		}
	})

	t.Run("Placeholder", func(t *testing.T) {
		for _, n := range []int{1, 2, 10} {
			if got := d.Placeholder(n); got != "?" {
				t.Errorf("Placeholder(%d) = %q, want %q", n, got, "?")
			}
		}
	})

	t.Run("Now", func(t *testing.T) {
		if got := d.Now(); got != "datetime('now')" {
			t.Errorf("Now() = %q, want %q", got, "datetime('now')")
		}
	})

	t.Run("TimestampAfterNow", func(t *testing.T) {
		got := d.TimestampAfterNow(30)
		if got != "datetime('now', '+30 seconds')" {
			t.Errorf("TimestampAfterNow(30) = %q", got)
		}
	})

	t.Run("ClaimRunQuery uses question marks", func(t *testing.T) {
		q := d.ClaimRunQuery("", 30)
		if strings.Contains(q, "FOR UPDATE SKIP LOCKED") {
			t.Errorf("SQLite ClaimRunQuery should not contain FOR UPDATE SKIP LOCKED")
		}
		if !strings.Contains(q, "leased_by = ?") {
			t.Errorf("ClaimRunQuery missing ? placeholder")
		}
		if !strings.Contains(q, "+30 seconds") {
			t.Errorf("ClaimRunQuery missing embedded lease duration")
		}
	})

	t.Run("ClaimSchedulesQuery no row locking", func(t *testing.T) {
		q := d.ClaimSchedulesQuery()
		if strings.Contains(q, "FOR UPDATE") {
			t.Errorf("SQLite ClaimSchedulesQuery should not contain FOR UPDATE")
		}
		if !strings.Contains(q, "enabled = 1") {
			t.Errorf("ClaimSchedulesQuery missing enabled = 1")
		}
	})

	t.Run("MigrateSQL contains sqlite-specific syntax", func(t *testing.T) {
		sql := d.MigrateSQL()
		if !strings.Contains(sql, "TEXT PRIMARY KEY") {
			t.Errorf("MigrateSQL missing TEXT PRIMARY KEY")
		}
		if strings.Contains(sql, "JSONB") {
			t.Errorf("MigrateSQL should not contain JSONB for SQLite")
		}
	})
}
