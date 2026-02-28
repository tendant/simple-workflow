package simpleworkflow

import "strings"

// RewritePlaceholders converts PostgreSQL-style $N placeholders to ? placeholders.
// Example: "WHERE id = $1 AND status = $2" → "WHERE id = ? AND status = ?"
func RewritePlaceholders(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	i := 0
	for i < len(query) {
		if query[i] == '$' && i+1 < len(query) && query[i+1] >= '1' && query[i+1] <= '9' {
			b.WriteByte('?')
			i++ // skip '$'
			// skip all following digits
			for i < len(query) && query[i] >= '0' && query[i] <= '9' {
				i++
			}
		} else if query[i] == '\'' {
			// Skip string literals to avoid replacing $N inside them
			b.WriteByte(query[i])
			i++
			for i < len(query) && query[i] != '\'' {
				b.WriteByte(query[i])
				i++
			}
			if i < len(query) {
				b.WriteByte(query[i])
				i++
			}
		} else {
			b.WriteByte(query[i])
			i++
		}
	}
	return b.String()
}
