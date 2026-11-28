package translate

import (
	"encoding/json"
	"testing"
)

// A field the hub does not model means something only to the format it came
// from, so it rides through a body that goes out in that format and is dropped
// on a conversion: an OpenAI or Gemini upstream must not receive a Claude
// client's context_management.
func TestAnUnmodelledFieldRidesOnlyInItsOwnFormat(t *testing.T) {
	// A field no format models: the Claude writer has its own filter for
	// context_management, which the claude tests pin.
	body := []byte(`{"model":"m","max_tokens":9,"messages":[{"role":"user","content":"x"}],"x_vendor_flag":true}`)
	for name, tc := range map[string]struct {
		to   string
		want bool
	}{
		"the same format": {"claude", true},
		"a conversion":    {"openai", false},
		"gemini":          {"gemini", false},
	} {
		out, _, err := Convert("claude", tc.to, body, NewCtx("claude", tc.to))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: %s", name, out)
		}
		if _, present := got["x_vendor_flag"]; present != tc.want {
			t.Errorf("%s: x_vendor_flag present %v, want %v: %s", name, present, tc.want, out)
		}
	}
}

func TestPrepareDropsWhatTheTargetFormatCannotCarry(t *testing.T) {
	r := &Request{Messages: []Message{user("x")}, Extra: map[string]json.RawMessage{"output_config": json.RawMessage(`{"format":"x"}`)}}
	Prepare(r, NewCtx("claude", "openai"))
	if r.Extra != nil {
		t.Fatalf("a converted request kept its foreign fields: %v", r.Extra)
	}
	kept := &Request{Messages: []Message{user("x")}, Extra: map[string]json.RawMessage{"output_config": json.RawMessage(`{"format":"x"}`)}}
	Prepare(kept, NewCtx("claude", "claude"))
	if kept.Extra == nil {
		t.Fatal("a same-format request lost its unmodelled fields")
	}
}
