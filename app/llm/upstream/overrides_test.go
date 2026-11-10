package upstream

import "testing"

func TestHeaderOverrides(t *testing.T) {
	raw := `{"openai":{"headers":{"X-A":"1","X-N":2}},"we.ird*id":{"headers":{"X-B":"x"}},"bad":5}`
	cases := []struct {
		provider string
		want     map[string]string
	}{
		{"openai", map[string]string{"X-A": "1"}},
		{"we.ird*id", map[string]string{"X-B": "x"}},
		{"bad", map[string]string{}},
		{"none", map[string]string{}},
	}
	for _, c := range cases {
		got := HeaderOverrides(raw, c.provider)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.provider, got, c.want)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: got %v, want %v", c.provider, got, c.want)
			}
		}
	}
	if len(HeaderOverrides("", "openai")) != 0 || len(HeaderOverrides("not json", "openai")) != 0 {
		t.Fatal("unreadable input must give no headers")
	}
}
