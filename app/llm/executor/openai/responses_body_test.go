package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/store/document"
)

func TestKeepOnlyDropsEveryOtherTopLevelField(t *testing.T) {
	out := keepOnly([]byte(`{"model":"m","input":[],"temperature":1,"a.b":2,"x*":3}`), map[string]bool{"model": true, "input": true})
	if string(out) != `{"model":"m","input":[]}` {
		t.Fatalf("%s", out)
	}
}

func shapeTools(t *testing.T, tools string) (string, map[string]bool) {
	t.Helper()
	out, names := flattenTools([]byte(`{"tools":`+tools+`}`), stripUnicodePatterns, "")
	return gjson.GetBytes(out, "tools").Raw, names
}

func TestToolsAreFlattenedHostedOnesKeptAndOthersDropped(t *testing.T) {
	got, names := shapeTools(t, `[
	  {"type":"function","function":{"name":"  read  ","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}},"strict":true}},
	  {"type":"web_search"},
	  {"type":"unknown_kind","name":"x"},
	  {"type":"function","function":{"name":""}},
	  "junk",
	  {"type":"function","name":"flat","parameters":{"type":"object"}}]`)
	g := gjson.Parse(got)
	if len(g.Array()) != 3 {
		t.Fatalf("%d tools kept: %s", len(g.Array()), got)
	}
	if g.Get("0.name").String() != "read" || g.Get("0.description").String() != "d" || !g.Get("0.strict").Bool() || g.Get("0.function").Exists() {
		t.Fatalf("first tool: %s", g.Get("0").Raw)
	}
	if g.Get("1.type").String() != "web_search" || g.Get("2.name").String() != "flat" {
		t.Fatalf("%s", got)
	}
	if !names["read"] || !names["flat"] || names["x"] {
		t.Fatalf("names %v", names)
	}
}

func TestAToolWithNoSchemaGetsAnEmptyObjectSchema(t *testing.T) {
	got, _ := shapeTools(t, `[{"type":"function","name":"n"}]`)
	if gjson.Get(got, "0.parameters.type").String() != "object" {
		t.Fatalf("%s", got)
	}
}

func TestAToolNameIsCutAt128(t *testing.T) {
	long := strings.Repeat("a", 200)
	got, names := shapeTools(t, `[{"type":"function","name":"`+long+`"}]`)
	if len(gjson.Get(got, "0.name").String()) != 128 || !names[long[:128]] {
		t.Fatalf("%s", got)
	}
}

func TestAForcedChoiceThatNamesNoToolGoes(t *testing.T) {
	names := map[string]bool{"read": true}
	keep := fixToolChoice([]byte(`{"tool_choice":{"type":"function","name":"read"}}`), names)
	if !gjson.GetBytes(keep, "tool_choice").Exists() {
		t.Fatal("a valid choice was dropped")
	}
	for _, in := range []string{`{"tool_choice":{"type":"function","name":"ghost"}}`, `{"tool_choice":{"type":"function"}}`} {
		if gjson.GetBytes(fixToolChoice([]byte(in), names), "tool_choice").Exists() {
			t.Fatalf("%s kept", in)
		}
	}
	if string(fixToolChoice([]byte(`{"tool_choice":"auto"}`), names)) != `{"tool_choice":"auto"}` {
		t.Fatal("a string choice changed")
	}
}

func TestUnicodePropertyPatternsAreRemovedAtAnyDepth(t *testing.T) {
	in := `{"type":"object","properties":{"a":{"type":"string","pattern":"^[^\\p{Cc}]+$"},"b":{"type":"array","items":{"type":"string","pattern":"\\P{L}"}}},"anyOf":[{"pattern":"\\p{Lu}","type":"string"}]}`
	out := stripUnicodePatterns(in)
	if strings.Contains(out, "pattern") || !gjson.Valid(out) {
		t.Fatalf("%s", out)
	}
	if gjson.Get(out, "properties.a.type").String() != "string" || gjson.Get(out, "anyOf.0.type").String() != "string" {
		t.Fatalf("the rest of the schema was damaged: %s", out)
	}
}

func TestOrdinaryPatternsAndEscapedBackslashesStayByteForByte(t *testing.T) {
	for _, in := range []string{
		`{"type":"string","pattern":"^[a-z]+$"}`,
		`{"type":"string","pattern":"\\\\p{x}"}`,
		`{"type":"object","properties":{"a":{"pattern":"^\\d+$"}}}`,
	} {
		if got := stripUnicodePatterns(in); got != in {
			t.Errorf("changed:\n%s\n%s", in, got)
		}
	}
}

func TestAPropertyNamedPatternIsNotAKeyword(t *testing.T) {
	in := `{"type":"object","properties":{"pattern":{"type":"string"},"properties":{"type":"string","pattern":"\\p{L}"}}}`
	out := stripUnicodePatterns(in)
	if !gjson.Get(out, "properties.pattern").Exists() || !gjson.Get(out, "properties.properties").Exists() {
		t.Fatalf("a property was removed: %s", out)
	}
	if gjson.Get(out, `properties.properties.pattern`).Exists() {
		t.Fatalf("the real constraint stayed: %s", out)
	}
}

func FuzzStripUnicodePatterns(f *testing.F) {
	for _, s := range []string{`{}`, `{"pattern":"\\p{L}"}`, `{"properties":{"pattern":{"pattern":"\\P{x}"}}}`, `[{"pattern":"a"}]`, `"x"`, `{"a":`, ``} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := stripUnicodePatterns(s)
		if !gjson.Valid(s) {
			return
		}
		if !gjson.Valid(out) {
			t.Fatalf("invalid JSON out for %q: %q", s, out)
		}
		if again := stripUnicodePatterns(out); again != out {
			t.Fatalf("not idempotent: %q then %q", out, again)
		}
	})
}

func TestKeepOnlyLeavesNothingBehindForAnOddFieldName(t *testing.T) {
	for _, name := range []string{``, `a.b`, `*`, `#`, `@this`, `a|b`, `\`} {
		in := `{"model":"m",` + document.Encode(name) + `:1}`
		out := keepOnly([]byte(in), map[string]bool{"model": true})
		if string(out) != `{"model":"m"}` {
			t.Errorf("%q: %s", name, out)
		}
	}
}

func TestAPropertyNamedPatternWhoseSchemaLooksLikeAConstraintIsKept(t *testing.T) {
	in := `{"type":"object","properties":{"pattern":"\\p{L}"}}`
	if got := stripUnicodePatterns(in); got != in {
		t.Fatalf("a property name was read as a keyword: %s", got)
	}
}
