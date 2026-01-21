package simpleworkflow

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseConnString extracts schema information from a connection string
// and returns a modified connection string with search_path parameter.
//
// Supports multiple formats:
//   - postgres://user:pass@host/db?schema=myschema
//   - postgres://user:pass@host/db?search_path=myschema
//   - postgres://user:pass@host/db (uses defaultSchema)
//
// Returns: (modifiedConnString, schemaName, error)
func ParseConnString(connString, defaultSchema string) (string, string, error) {
	// Handle non-URL format (e.g., "host=localhost user=postgres ...")
	if !strings.HasPrefix(connString, "postgres://") && !strings.HasPrefix(connString, "postgresql://") {
		// For non-URL format, check if search_path is already present
		if strings.Contains(connString, "search_path=") {
			// Extract schema from search_path
			parts := strings.Split(connString, "search_path=")
			if len(parts) > 1 {
				// Get everything after search_path= until space or end
				schemaEnd := strings.IndexAny(parts[1], " \t\n")
				schema := parts[1]
				if schemaEnd > 0 {
					schema = parts[1][:schemaEnd]
				}
				return connString, schema, nil
			}
		}
		// Add search_path to non-URL format
		modifiedConn := connString + " search_path=" + defaultSchema
		return modifiedConn, defaultSchema, nil
	}

	// Parse URL format
	u, err := url.Parse(connString)
	if err != nil {
		return "", "", fmt.Errorf("invalid connection string: %w", err)
	}

	query := u.Query()
	schema := defaultSchema

	// Check for search_path parameter (standard PostgreSQL parameter)
	if sp := query.Get("search_path"); sp != "" {
		schema = sp
		return connString, schema, nil
	}

	// Check for custom schema parameter (our convenience parameter)
	if s := query.Get("schema"); s != "" {
		schema = s
		// Remove schema parameter and add search_path
		query.Del("schema")
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		return u.String(), schema, nil
	}

	// No schema specified, add default search_path
	query.Set("search_path", defaultSchema)
	u.RawQuery = query.Encode()
	return u.String(), defaultSchema, nil
}

// DefaultSchema is the default schema name if not specified
const DefaultSchema = "workflow"
