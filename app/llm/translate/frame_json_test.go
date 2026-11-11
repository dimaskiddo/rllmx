package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendJSONStringIsWhatEncodingJSONWrites(t *testing.T) {
	for _, s := range []string{
		"", "plain", `quote " and \ slash`, "a\nb\r\tc\b\f", "\x00\x01\x1f\x7f", "<tag>&amp;", "caf\u00e9 \u4e16\u754c \U0001F600",
		"line\u2028sep\u2029para", "bad \xff\xfe bytes", "cut \xe2\x82", "\xed\xa0\x80 surrogate", "mix \"\u2028\xff<\n",
	} {
		want, _ := json.Marshal(s)
		if got := AppendJSONString(nil, s); string(got) != string(want) {
			t.Errorf("%q:\n got  %s\n want %s", s, got, want)
		}
	}
}

func FuzzAppendJSONString(f *testing.F) {
	for _, s := range []string{"", "a", "\u2028", "\xff", "<>&\"\\\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, _ := json.Marshal(s)
		prefix := []byte("keep:")
		got := AppendJSONString(prefix, s)
		if string(got) != "keep:"+string(want) {
			t.Fatalf("%q:\n got  %s\n want %s", s, got, want)
		}
	})
}

func TestAStrictClientGetsAnIDItAccepts(t *testing.T) {
	for _, weak := range []string{"", "chat", "completion", "short", "1234567"} {
		if !WeakStreamID(weak) {
			t.Errorf("%q should be weak", weak)
		}
		got := StrictStreamID(weak)
		if !strings.HasPrefix(got, "chatcmpl-") || WeakStreamID(got) {
			t.Errorf("%q became %q", weak, got)
		}
	}
	for _, ok := range []string{"chatcmpl-abc12345", "resp_0123456789", "12345678"} {
		if WeakStreamID(ok) || StrictStreamID(ok) != ok {
			t.Errorf("%q was replaced", ok)
		}
	}
	first, second := StrictStreamID(""), StrictStreamID("")
	if first == second {
		t.Error("two generated ids are equal")
	}
}
