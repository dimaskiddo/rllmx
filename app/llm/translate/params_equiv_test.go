package translate

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/tidwall/gjson"
	"testing"

	"rllmx/app/llm/upstream"
)

func sameJSON(t *testing.T, name string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Errorf("%s: output is not JSON: %v\n%s", name, err, got)
		return
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: reference is not JSON: %v", name, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got  %s\n want %s", name, got, want)
	}
}

func TestApplyParamsMeansWhatTheMapRoundTripMeant(t *testing.T) {
	bodies := map[string]string{
		"plain":          `{"max_tokens":9000,"messages":[{"content":"x","role":"user"}],"model":"m","temperature":0.5,"top_p":1}`,
		"flatten":        `{"messages":[{"content":[{"text":"a","type":"text"},{"text":"b","type":"text"}],"reasoning_content":"r","role":"assistant"},{"content":[{"text":"x","type":"text"},{"image_url":{"url":"u"},"type":"image_url"}],"role":"user"}],"model":"m"}`,
		"empty parts":    `{"messages":[{"content":[],"role":"assistant"}],"model":"m"}`,
		"cap string":     `{"max_completion_tokens":"9000","max_tokens":9000,"model":"m"}`,
		"cap float":      `{"max_tokens":9000.5,"model":"m"}`,
		"rename taken":   `{"model":"m","new":2,"old":1}`,
		"rename free":    `{"model":"m","old":1}`,
		"odd names":      `{"a.b":1,"model":"m","x*y":2,"z#":3}`,
		"no messages":    `{"model":"m"}`,
		"bad messages":   `{"messages":"x","model":"m"}`,
		"scalar message": `{"messages":[1,"a",{"content":[{"text":"q","type":"text"}],"role":"assistant"}],"model":"m"}`,
	}
	rules := [][]upstream.ParamRule{
		{{Drop: []string{"temperature", "top_p", "a.b", "x*y"}}},
		{{Rename: map[string]string{"old": "new"}}},
		{{DropMessageFields: []string{"reasoning_content"}, FlattenContent: true}},
		{{CapOutput: 4096}},
		{{Drop: []string{"z#"}}, {Rename: map[string]string{"a.b": "c"}}, {CapOutput: 10}},
		{{DropMessageFields: []string{"role"}}},
	}
	for name, in := range bodies {
		for i, rs := range rules {
			got := ApplyParams([]byte(in), rs)
			want := applyParamsReference([]byte(in), rs)
			sameJSON(t, name+"/"+string(rune('0'+i)), got, want)
		}
	}
}

func TestApplyParamsKeepsOrderNumbersAndUnknownFields(t *testing.T) {
	in := `{"zeta":1e400,"alpha":12345678901234567890,"temperature":0.1,"model":"m","big":1.0}`
	got := string(ApplyParams([]byte(in), []upstream.ParamRule{{Drop: []string{"temperature"}}}))
	if want := `{"zeta":1e400,"alpha":12345678901234567890,"model":"m","big":1.0}`; got != want {
		t.Fatalf("\n got  %s\n want %s", got, want)
	}
}

func TestACapLeavesASmallerLimitAndOddNumbersAlone(t *testing.T) {
	rs := []upstream.ParamRule{{CapOutput: 4096}}
	for in, want := range map[string]string{
		`{"max_tokens":500}`:                            `{"max_tokens":500}`,
		`{"max_tokens":4096}`:                           `{"max_tokens":4096}`,
		`{"max_tokens":4097}`:                           `{"max_tokens":4096}`,
		`{"max_tokens":1e9}`:                            `{"max_tokens":1e9}`,
		`{"max_tokens":"9000"}`:                         `{"max_tokens":"9000"}`,
		`{"max_completion_tokens":9000,"max_tokens":3}`: `{"max_completion_tokens":4096,"max_tokens":3}`,
	} {
		if got := string(ApplyParams([]byte(in), rs)); got != want {
			t.Errorf("%s\n got  %s\n want %s", in, got, want)
		}
	}
}

func TestANullMessageStaysNull(t *testing.T) {
	in := `{"messages":[null,{"content":[{"text":"q","type":"text"}],"role":"assistant"}]}`
	got := string(ApplyParams([]byte(in), []upstream.ParamRule{{FlattenContent: true}}))
	if want := `{"messages":[null,{"content":"q","role":"assistant"}]}`; got != want {
		t.Fatalf("\n got  %s\n want %s", got, want)
	}
	if got := string(ApplyParams([]byte(`{"messages":[1,{"role":"assistant","x":1}]}`), []upstream.ParamRule{{DropMessageFields: []string{"x"}}})); got != `{"messages":[1,{"role":"assistant","x":1}]}` {
		t.Fatalf("a body with a scalar message was rewritten: %s", got)
	}
}

func TestFlattenReadsExactFieldNames(t *testing.T) {
	in := `{"messages":[{"content":[{"teXt":"0","tYpe":"text"}],"role":"assistant"}],"model":"m"}`
	got := string(ApplyParams([]byte(in), []upstream.ParamRule{{FlattenContent: true}}))
	if got != in {
		t.Fatalf("a part with no exact type field was flattened:\n got  %s", got)
	}
}

func TestApplyParamsLeavesAnArrayOrScalarBodyAlone(t *testing.T) {
	for _, in := range []string{`[1,2]`, `"x"`, `7`, `null`, ``, `{"a":`} {
		if got := string(ApplyParams([]byte(in), []upstream.ParamRule{{Drop: []string{"a"}}})); got != in {
			t.Errorf("%q became %q", in, got)
		}
	}
}

func FuzzApplyParamsMeansWhatTheMapRoundTripMeant(f *testing.F) {
	f.Add(`{"max_tokens":9000,"messages":[{"content":[{"text":"a","type":"text"}],"role":"assistant"}],"temperature":1}`)
	f.Add(`{"a.b":1,"messages":[null,{"role":"assistant"}]}`)
	rs := []upstream.ParamRule{{Drop: []string{"temperature", "a.b"}, Rename: map[string]string{"old": "new"}, DropMessageFields: []string{"reasoning_content"}, FlattenContent: true, CapOutput: 100}}
	f.Fuzz(func(t *testing.T, in string) {
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(in), &probe) != nil || len(probe) == 0 {
			return
		}
		// Duplicate keys make a map keep one value and an in-place edit keep
		// both, so they are outside the comparison.
		// A null message stays null here; the map version made it an empty object.
		for _, m := range gjson.Get(in, "messages").Array() {
			if m.Type == gjson.Null {
				return
			}
		}
		var pairs int
		dec := json.NewDecoder(strings.NewReader(in))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return
		}
		for dec.More() {
			if _, err := dec.Token(); err != nil {
				return
			}
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return
			}
			pairs++
		}
		if pairs != len(probe) {
			return
		}
		// encoding/json matches "teXt" to a field tagged "text", gjson does not;
		// the codecs write exact lower-case names, so only those are compared.
		if strings.ContainsAny(in, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return
		}
		got := ApplyParams([]byte(in), rs)
		want := applyParamsReference([]byte(in), rs)
		var g, w any
		if json.Unmarshal(got, &g) != nil || json.Unmarshal(want, &w) != nil {
			t.Fatalf("%q: not JSON\n got  %s\n want %s", in, got, want)
		}
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("%q:\n got  %s\n want %s", in, got, want)
		}
	})
}
