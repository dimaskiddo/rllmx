package dialect

import (
	"strings"
	"testing"
	"time"
)

func TestRebind(t *testing.T) {
	tests := []struct {
		name string
		in   string
		d    Dialect
		want string
	}{
		{"sqlite is untouched", `SELECT * FROM "t" WHERE a = ? AND b = ?`, SQLite, `SELECT * FROM "t" WHERE a = ? AND b = ?`},
		{"none", `SELECT 1`, Postgres, `SELECT 1`},
		{"one", `SELECT * FROM "t" WHERE a = ?`, Postgres, `SELECT * FROM "t" WHERE a = $1`},
		{"many", `INSERT INTO "t"(a,b,c) VALUES (?,?,?)`, Postgres, `INSERT INTO "t"(a,b,c) VALUES ($1,$2,$3)`},
		{"question mark in a string literal", `SELECT '?' , ?`, Postgres, `SELECT '?' , $1`},
		{"escaped quote in a literal", `SELECT 'it''s ?', ?`, Postgres, `SELECT 'it''s ?', $1`},
		{"question mark in a quoted identifier", `SELECT "a?b" FROM t WHERE x = ?`, Postgres, `SELECT "a?b" FROM t WHERE x = $1`},
		{"escaped quote in an identifier", `SELECT "a""?b", ?`, Postgres, `SELECT "a""?b", $1`},
		{"question mark right after a literal", `SELECT 'x'?`, Postgres, `SELECT 'x'$1`},
		{"ten placeholders", `?,?,?,?,?,?,?,?,?,?`, Postgres, `$1,$2,$3,$4,$5,$6,$7,$8,$9,$10`},
		{"unterminated literal keeps the rest", `SELECT 'abc ?`, Postgres, `SELECT 'abc ?`},
		{"empty", ``, Postgres, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Rebind(tt.in, tt.d); got != tt.want {
				t.Fatalf("Rebind(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripNUL(t *testing.T) {
	if got := StripNUL("a\x00b\x00"); got != "ab" {
		t.Fatalf("got %q", got)
	}
	if got := StripNUL("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatTimestampKeepsMillisecondsAndZ(t *testing.T) {
	tm, err := time.Parse(time.RFC3339Nano, "2026-10-29T07:19:30.200Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatTimestamp(tm); got != "2026-10-29T07:19:30.200Z" {
		t.Fatalf("trailing zeros lost: %q", got)
	}
	tm = tm.Truncate(time.Second)
	if got := FormatTimestamp(tm); got != "2026-10-29T07:19:30.000Z" {
		t.Fatalf("zero milliseconds lost: %q", got)
	}
	loc := time.FixedZone("WIB", 7*3600)
	if got := FormatTimestamp(tm.In(loc)); got != "2026-10-29T07:19:30.000Z" {
		t.Fatalf("not converted to UTC: %q", got)
	}
}

func TestParseTimestampRoundTrips(t *testing.T) {
	const in = "2026-10-29T07:19:30.200Z"
	tm, err := ParseTimestamp(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatTimestamp(tm); got != in {
		t.Fatalf("got %q", got)
	}
	if _, err := ParseTimestamp("2026-10-29"); err == nil {
		t.Fatal("a date without a time was accepted")
	}
}

func TestNewIDIsLowercaseV4(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewID()
		if len(id) != 36 || id[14] != '4' || id != strings.ToLower(id) {
			t.Fatalf("not a lowercase v4 uuid: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %q", id)
		}
		seen[id] = true
	}
}

func TestNullTextBindsEmptyAndNULOnlyAsNull(t *testing.T) {
	for in, want := range map[string]any{"": nil, "\x00\x00": nil, "a\x00b": "ab", "x": "x"} {
		if got := NullText(in); got != want {
			t.Errorf("NullText(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBoolInt(t *testing.T) {
	if BoolInt(true) != 1 || BoolInt(false) != 0 {
		t.Fatal("BoolInt must be 1 for true and 0 for false")
	}
}

func TestDialectOf(t *testing.T) {
	for name, want := range map[string]Dialect{"sqlite": SQLite, "sqlite_ro": SQLite, "sqlite_analytics": SQLite, "postgres": Postgres, "postgres_analytics": Postgres} {
		got, err := Of(name)
		if err != nil || got != want {
			t.Errorf("Of(%q) = %v, %v", name, got, err)
		}
	}
	for _, bad := range []string{"mysql", "", "sqlitex", "my_sqlite"} {
		if _, err := Of(bad); err == nil {
			t.Errorf("Of(%q) was accepted", bad)
		}
	}
}
