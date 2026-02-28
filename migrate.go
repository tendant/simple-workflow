package simpleworkflow

import (
	"context"
	"fmt"
)

// AutoMigrate runs the embedded DDL for the current dialect.
// This creates all required tables if they don't exist.
// For PostgreSQL users who prefer goose, this is optional.
// For SQLite users, this is the recommended way to set up the schema.
func (c *Client) AutoMigrate(ctx context.Context) error {
	ddl := c.dialect.MigrateSQL()
	if _, err := c.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("auto-migrate failed: %w", err)
	}
	return nil
}
