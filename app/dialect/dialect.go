// Package dialect holds the SQL details that differ between the two engines and
// the stored-value formats: placeholder rebinding, NUL handling, booleans,
// timestamps and row ids. Nothing here touches a connection.
package dialect

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Dialect selects the SQL details that differ between the two engines.
type Dialect int

// Supported engines.
const (
	SQLite Dialect = iota
	Postgres
)

// BusyWait is the DSN pragma that makes a locked SQLite file wait instead of
// failing. It is the same value in config/database.go and in every one-off open
// of a foreign file, so a command and the server queue behind the same writer.
const BusyWait = "_pragma=busy_timeout(5000)"

const timestampLayout = "2006-01-02T15:04:05.000Z"

// Of maps a Goravel connection name to its engine: "sqlite", "postgres" and the
// variants config/database.go declares (sqlite_ro, sqlite_analytics,
// postgres_analytics).
func Of(connection string) (Dialect, error) {
	switch {
	case connection == "sqlite" || strings.HasPrefix(connection, "sqlite_"):
		return SQLite, nil
	case connection == "postgres" || strings.HasPrefix(connection, "postgres_"):
		return Postgres, nil
	}
	return SQLite, fmt.Errorf("dialect: unsupported database connection %q", connection)
}

// Rebind rewrites ? placeholders to $1, $2... for PostgreSQL, whose Goravel
// driver passes SQL through and understands only the numbered form. Question
// marks inside string literals and quoted identifiers stay as they are; a
// doubled quote closes and reopens the quote, which has the same effect as an
// escape.
func Rebind(q string, d Dialect) string {
	if d != Postgres || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	var quote byte
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			b.WriteByte(c)
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// StripNUL removes NUL bytes, which PostgreSQL cannot store in text.
func StripNUL(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// NullText binds an empty string, after NUL removal, as SQL NULL.
func NullText(s string) any {
	if s = StripNUL(s); s == "" {
		return nil
	}
	return s
}

// BoolInt is the 0 or 1 a boolean column holds.
func BoolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// FormatTimestamp renders t the way the shared schema stores it: UTC,
// milliseconds, Z.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(timestampLayout) }

// ParseTimestamp reads a timestamp in the stored form.
func ParseTimestamp(s string) (time.Time, error) { return time.Parse(timestampLayout, s) }

// Now is the current time in the stored form.
func Now() string { return FormatTimestamp(time.Now()) }

// NewID returns a lowercase version 4 UUID.
func NewID() string { return uuid.NewString() }
