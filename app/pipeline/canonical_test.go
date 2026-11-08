package pipeline

import (
	"bytes"
	"strings"
	"testing"
)

func canon(t *testing.T, in string) string {
	t.Helper()
	out, ok := canonicalBody([]byte(in))
	if !ok {
		t.Fatalf("refused %s", in)
	}
	return string(out)
}

func TestCanonicalBodyIgnoresKeyOrderAndTheStreamFlag(t *testing.T) {
	a := canon(t, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	b := canon(t, `{"temperature":0,"messages":[{"content":"hi","role":"user"}],"stream":false,"model":"m"}`)
	c := canon(t, `{"temperature":0,"messages":[{"content":"hi","role":"user"}],"model":"m"}`)
	if a != b || b != c {
		t.Fatalf("%s\n%s\n%s", a, b, c)
	}
	if strings.Contains(a, "stream") {
		t.Fatalf("stream kept: %s", a)
	}
}

func TestCanonicalBodyKeepsWhatChangesTheAnswer(t *testing.T) {
	base := canon(t, `{"a":[1,2],"n":1}`)
	for _, other := range []string{`{"a":[2,1],"n":1}`, `{"a":[1,2],"n":1.0}`, `{"a":[1,2],"n":2}`, `{"a":[1,2],"n":1,"x":null}`, `{"a":[1,2],"n":"1"}`} {
		if canon(t, other) == base {
			t.Errorf("%s collapsed into %s", other, base)
		}
	}
	if got := canon(t, `{"nested":{"stream":true,"b":1,"a":2}}`); got != `{"nested":{"a":2,"b":1,"stream":true}}` {
		t.Errorf("a nested stream key must stay: %s", got)
	}
	if got := canon(t, `{"big":12345678901234567890123,"f":1e400}`); got != `{"big":12345678901234567890123,"f":1e400}` {
		t.Errorf("number text changed: %s", got)
	}
}

func TestCanonicalBodyRefusesWhatHasNoSingleMeaning(t *testing.T) {
	for _, in := range []string{
		``, `[]`, `"x"`, `{`, `{"a":1,}`, `{"a":1}{"b":2}`, `{"a":1} x`, `{"a":1,"a":2}`, `{"o":{"p":{"q":1,"q":2}}}`,
		`{"arr":[{"k":1,"k":1}]}`, `{"stream":true,"stream":false}`, `{"a":}`, `{"a":[1,}`,
	} {
		if out, ok := canonicalBody([]byte(in)); ok {
			t.Errorf("%q was accepted as %s", in, out)
		}
	}
}

func TestCanonicalBodyIsIdempotent(t *testing.T) {
	once := canon(t, `{"z":{"b":[{"y":1,"x":2}],"a":"é\n\u0000"},"stream":true}`)
	if twice := canon(t, once); twice != once {
		t.Fatalf("%s != %s", once, twice)
	}
}

func TestCacheKeyPartsDoNotRunTogether(t *testing.T) {
	if cacheKey("ab", "c") == cacheKey("a", "bc") || cacheKey("a", "") == cacheKey("", "a") || cacheKey("a") == cacheKey("a", "") {
		t.Fatal("two part lists share a key")
	}
	parts := []string{"x", "y"}
	if cacheKey(parts...) != cacheKey(append([]string(nil), parts...)...) {
		t.Fatal("the key is not stable")
	}
}

func FuzzCanonicalBody(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"b":{"c":[1,{"d":2}]},"stream":true}`, `{"a":1,"a":2}`, `[]`, `{"x":"\u0000"}`, ``} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, ok := canonicalBody(in)
		if !ok {
			return
		}
		again, ok := canonicalBody(out)
		if !ok || !bytes.Equal(again, out) {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, again)
		}
		if bytes.Contains(out, []byte(`"stream":`)) && !bytes.Contains(in, []byte(`stream`)) {
			t.Fatalf("a stream key appeared from nothing: %q", out)
		}
	})
}
