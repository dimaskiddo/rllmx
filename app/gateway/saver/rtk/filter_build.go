package rtk

import (
	"fmt"
	"regexp"
	"strings"
)

// Build-tool shapes.
var (
	reDeprecated = regexp.MustCompile(`(?i)^npm warn deprecated`)
	reBuildErr   = regexp.MustCompile(`^error(\[|:)|^error -->`)
	reBuildWarn  = regexp.MustCompile(`^warning(\[|:)|^warning -->`)
	reCompiled   = regexp.MustCompile(`(?i)^\s*Compiling\s+\S+`)
	reFetched    = regexp.MustCompile(`(?i)^\s*Downloading\s+\S+|^Fetching\s+`)
	reCargoCont  = regexp.MustCompile(`^\s*(-->|\||\d+\s*\||=)`)
	reBuildSum   = regexp.MustCompile(`(?i)^(added|removed|changed|audited|installed)\s+\d+\s+package|^\s*Finished\s+|^BUILD SUCCESS|^\d+\s+(vulnerabilities|packages?|warnings?|errors?)|^Successfully (installed|built)|^To address .* issues|^Run ` + "`npm (audit|fund)`" + `|packages are looking for funding`)
)

// buildLine is what one log line means to the filter.
type buildLine int

const (
	buildSkip buildLine = iota
	buildErrorLine
	buildWarnLine
	buildDeprecated
	buildSummary
	buildCompiled
	buildFetched
)

// classifyBuild sorts one trimmed line, in the order the shapes overlap: the
// package-manager and compiler forms first, then the batch summary.
func classifyBuild(t string) buildLine {
	switch {
	case t == "":
		return buildSkip
	case isNpmError(t):
		return buildErrorLine
	case reDeprecated.MatchString(t):
		return buildDeprecated
	case isNpmWarn(t):
		return buildWarnLine
	case reBuildErr.MatchString(t), strings.HasPrefix(t, "ERROR:"), strings.HasPrefix(t, "[ERROR]"), strings.HasPrefix(t, "BUILD FAILED"):
		return buildErrorLine
	case reBuildWarn.MatchString(t), strings.HasPrefix(t, "[WARNING]"):
		return buildWarnLine
	case reCompiled.MatchString(t):
		return buildCompiled
	case reFetched.MatchString(t):
		return buildFetched
	case reBuildSum.MatchString(t):
		return buildSummary
	}
	return buildSkip
}

// buildOutput keeps a build's errors, warnings and final summary, and collapses
// the progress lines that dominate a compiler or installer log.
func buildOutput(in string) string {
	var errorsOut, warnings, deprecated, summary []string
	compiled, fetched := 0, 0
	inCargoError := false

	for line := range strings.SplitSeq(in, "\n") {
		trimmed := strings.TrimSpace(line)
		if inCargoError {
			if trimmed == "" {
				inCargoError = false
				continue
			}
			if reCargoCont.MatchString(line) {
				errorsOut = append(errorsOut, line)
				continue
			}
			inCargoError = false
		}
		switch classifyBuild(trimmed) {
		case buildErrorLine:
			errorsOut = append(errorsOut, line)
			inCargoError = reBuildErr.MatchString(trimmed)
		case buildWarnLine:
			warnings = append(warnings, line)
			inCargoError = reBuildWarn.MatchString(trimmed)
		case buildDeprecated:
			deprecated = append(deprecated, line)
		case buildCompiled:
			compiled++
		case buildFetched:
			fetched++
		case buildSummary:
			summary = append(summary, line)
		}
	}

	var b strings.Builder
	for _, d := range deprecated[:min(len(deprecated), keepDeprecation)] {
		b.WriteString(d)
		b.WriteByte('\n')
	}
	if len(deprecated) > keepDeprecation {
		fmt.Fprintf(&b, "... +%d more deprecated packages\n", len(deprecated)-keepDeprecation)
	}
	if compiled > 0 {
		fmt.Fprintf(&b, "Compiled %d packages\n", compiled)
	}
	if fetched > 0 {
		fmt.Fprintf(&b, "Downloaded %d packages\n", fetched)
	}
	for _, e := range errorsOut {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	for _, w := range warnings[:min(len(warnings), keepWarnings)] {
		b.WriteString(w)
		b.WriteByte('\n')
	}
	if len(warnings) > keepWarnings {
		fmt.Fprintf(&b, "... +%d more warnings\n", len(warnings)-keepWarnings)
	}
	for _, s := range summary {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		return in
	}
	return out
}

func isNpmError(t string) bool {
	return strings.HasPrefix(t, "npm ERR!") || strings.HasPrefix(t, "npm error") || strings.HasPrefix(t, "yarn error")
}

func isNpmWarn(t string) bool {
	return strings.HasPrefix(t, "npm warn") || strings.HasPrefix(t, "yarn warn")
}
