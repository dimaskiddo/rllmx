package tests

import "strings"

// SplitStatements cuts a legacy DDL block at its semicolons, so a test can run
// it through the statement API one call at a time. Blank pieces are dropped.
func SplitStatements(ddl string) []string {
	var out []string
	for _, s := range strings.Split(ddl, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
