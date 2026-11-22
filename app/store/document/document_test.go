package document

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// keys taken from real model-lock names plus every ASCII punctuation class a model id can contain
var nastyKeys = []string{
	"modelLock_dpe/unlimited/gpt-6-luna", "modelLock_google/gemma-4-31b-it:free", "modelLock_gemini-4-argon-high",
	"modelLock_dadang:latest", "modelLock_a.b.c", "modelLock_x*y", "modelLock_q?z", "modelLock_a|b", "modelLock_#1",
	"modelLock_@now", "modelLock_a\\b", "modelLock_a b", "modelLock_\u00e9\u65e5\u672c", "modelLock___all", "modelLock_a=b",
	"modelLock_<x>", "modelLock_[0]", "modelLock_{a}", "modelLock_a,b", "modelLock_!", "modelLock_%", "modelLock_(x)",
	"taste-skillEnabled", "a\"quote",
}

func TestDocKeysRoundTrip(t *testing.T) {
	for _, k := range nastyKeys {
		d, err := ParseDoc(`{"id":"c1","testStatus":"active","providerSpecificData":{"k":1}}`)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetString(k, "2026-01-01T00:00:00.000Z"); err != nil {
			t.Fatalf("%q: %v", k, err)
		}
		if got, ok := d.GetString(k); !ok || got != "2026-01-01T00:00:00.000Z" {
			t.Fatalf("%q: read back %q %v; raw=%s", k, got, ok, d.String())
		}
		if n := len(d.Keys()); n != 4 {
			t.Fatalf("%q produced %d top-level keys, want 4: %v", k, n, d.Keys())
		}
		if err := d.SetNull(k); err != nil || !d.IsNull(k) || !d.Has(k) {
			t.Fatalf("%q: null-set failed: %v raw=%s", k, err, d.String())
		}
		if err := d.Delete(k); err != nil || d.Has(k) {
			t.Fatalf("%q: delete failed: %v raw=%s", k, err, d.String())
		}
		if n := len(d.Keys()); n != 3 {
			t.Fatalf("%q: delete changed other keys: %v", k, d.Keys())
		}
	}
}

func TestDocUntouchedBytesStayIdentical(t *testing.T) {
	raw := "{ \"b\" : 1e400 , \"a\":12345678901234567890,\"u\":\"\\u2028 \u65e5\u672c \\u00e9\",\"z\":{\"k\":[1, 2 ,3]},\"h\":\"<a>&amp;</a>\" }"
	d, err := ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetString("added", "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.String(), raw[:len(raw)-2]) {
		t.Fatalf("existing bytes changed:\n in : %s\n out: %s", raw, d.String())
	}
	d2, _ := ParseDoc(raw)
	if err := d2.SetInt("a", 7); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(raw, "12345678901234567890", "7", 1)
	if d2.String() != want {
		t.Fatalf("replace touched more than the value:\n want %s\n got  %s", want, d2.String())
	}
}

func TestDocWritesHTMLCharactersRaw(t *testing.T) {
	d, _ := ParseDoc(`{}`)
	if err := d.SetString("h", `<a href="x">&amp;</a>`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.String(), `\u003c`) || strings.Contains(d.String(), `\u0026`) {
		t.Fatalf("html characters were escaped: %s", d.String())
	}
	if got, _ := d.GetString("h"); got != `<a href="x">&amp;</a>` {
		t.Fatalf("read back %q", got)
	}
	if err := d.SetString("ls", "a\u2028b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("ls"); got != "a\u2028b" {
		t.Fatalf("U+2028 lost: %q", got)
	}
}

func TestDocNewKeyIsAppendedAndOldKeyKeepsItsPlace(t *testing.T) {
	d, _ := ParseDoc(`{"a":1,"b":2,"c":3}`)
	d.SetInt("b", 9)
	d.SetInt("z", 1)
	if d.String() != `{"a":1,"b":9,"c":3,"z":1}` {
		t.Fatalf("got %s", d.String())
	}
	d.Delete("a")
	d.SetInt("a", 5)
	if d.String() != `{"b":9,"c":3,"z":1,"a":5}` {
		t.Fatalf("got %s", d.String())
	}
}

func TestDocCorruptInputs(t *testing.T) {
	for _, in := range []string{``, `not json`, `{"a":`, `{"a":1,}`, `{"a":1} trailing`, `[1,2]`, `"str"`, `null`, `{"a":1,"a":2}`, `{"x":{"y":1},"x":3}`} {
		_, err := ParseDoc(in)
		if !errors.Is(err, ErrCorruptRow) {
			t.Errorf("ParseDoc(%q) err = %v, want ErrCorruptRow", in, err)
		}
	}
	// nested duplicate keys are legal JSON that a JavaScript reader reads the same way we do
	if _, err := ParseDoc(`{"a":{"k":1,"k":2}}`); err != nil {
		t.Errorf("nested duplicate key rejected: %v", err)
	}
}

func TestDocDeleteAbsentKeyIsANoOp(t *testing.T) {
	d, _ := ParseDoc(`{"a":1}`)
	if err := d.Delete("nope"); err != nil || d.String() != `{"a":1}` {
		t.Fatalf("%v %s", err, d.String())
	}
}

func TestDocSetRawKeepsABigNumber(t *testing.T) {
	d, _ := ParseDoc(`{}`)
	if err := d.SetRaw("n", "12345678901234567890123"); err != nil {
		t.Fatal(err)
	}
	if d.String() != `{"n":12345678901234567890123}` {
		t.Fatalf("got %s", d.String())
	}
	if err := d.SetRaw("bad", "{not json"); err == nil {
		t.Fatal("invalid raw value accepted")
	}
}

func TestDocBindsNoNULBytes(t *testing.T) {
	d, _ := ParseDoc(`{}`)
	if err := d.SetString("k", "a\x00b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("k"); got != "ab" {
		t.Fatalf("NUL reached the document: read back %q from %s", got, d.String())
	}
}

func FuzzDocEdit(f *testing.F) {
	f.Add("modelLock_a/b:c.d", "value")
	f.Add("a b", "<&>")
	f.Add("\u00e9", "\u2028")
	f.Add("k\\", "\"q\"")
	f.Fuzz(func(t *testing.T, key, val string) {
		if key == "" || !utf8.ValidString(key) || !utf8.ValidString(val) || strings.ContainsRune(key, 0) {
			t.Skip()
		}
		d, _ := ParseDoc(`{"keep":1,"other":{"x":2}}`)
		if err := d.SetString(key, val); err != nil {
			t.Skip()
		}
		if _, err := ParseDoc(d.String()); err != nil {
			t.Fatalf("edit produced a document that does not parse: %v\n%s", err, d.String())
		}
		if got, ok := d.GetString(key); !ok || got != strings.ReplaceAll(val, "\x00", "") {
			t.Fatalf("key %q read back %q %v", key, got, ok)
		}
		if !d.Has("keep") && key != "keep" {
			t.Fatalf("an unrelated key was lost editing %q", key)
		}
	})
}

func TestFirstStringSkipsEmptyAndNonStringValues(t *testing.T) {
	g := gjson.Parse(`{"n":7,"e":"","s":"x","t":true,"o":{"a":1}}`)
	if got := FirstString(g.Get("missing"), g.Get("e"), g.Get("n"), g.Get("t"), g.Get("o"), g.Get("s")); got != "x" {
		t.Fatalf("%q", got)
	}
	// A number's text is not a string, even though gjson can read it as one.
	if got := FirstString(g.Get("n"), g.Get("t")); got != "" {
		t.Fatalf("%q", got)
	}
	if got := FirstString(); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestTypedGettersTakeTheDefaultForAMissingOrWronglyTypedValue(t *testing.T) {
	d := From(`{"s":"x","b":true,"f":false,"n":3,"x":0.5,"str":"no","num":"7","null":null}`)
	if v := d.Str("s", "d"); v != "x" {
		t.Errorf("Str %q", v)
	}
	if v := d.Str("n", "d"); v != "d" {
		t.Errorf("a number read as a string: %q", v)
	}
	if v := d.Str("missing", "d"); v != "d" {
		t.Errorf("a missing key: %q", v)
	}
	if !d.Bool("b", false) || d.Bool("f", true) {
		t.Error("Bool did not read both values")
	}
	if !d.Bool("str", true) || d.Bool("num", false) || !d.Bool("null", true) || !d.Bool("missing", true) {
		t.Error("Bool did not take the default for the wrong type")
	}
	if d.Int("n", 9) != 3 || d.Int("num", 9) != 9 || d.Int("missing", 9) != 9 {
		t.Errorf("Int %d %d %d", d.Int("n", 9), d.Int("num", 9), d.Int("missing", 9))
	}
	if d.Float("x", 1) != 0.5 || d.Float("n", 1) != 3 || d.Float("str", 1) != 1 || d.Float("missing", 1) != 1 {
		t.Error("Float")
	}
}
