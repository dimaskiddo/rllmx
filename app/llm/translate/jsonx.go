package translate

import (
	"encoding/json"
	"sort"

	"github.com/tidwall/gjson"
)

// ExtraOf collects the top-level fields of a body that are not in known, so the
// rest can ride in Extra.
func ExtraOf(body []byte, known map[string]bool) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
		if known[k.String()] {
			return true
		}
		if out == nil {
			out = map[string]json.RawMessage{}
		}
		out[k.String()] = json.RawMessage(v.Raw)
		return true
	})
	return out
}

func Set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Obj builds a JSON object with its keys sorted, so equal requests encode to
// equal bytes. Go maps have no order and this is what keeps an upstream's
// prompt cache warm from one turn to the next.
type Obj map[string]any

func (o Obj) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(o[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		buf = append(buf, vb...)
	}
	return append(buf, '}'), nil
}

// FrameData encodes the object a stream writer built. Its values are strings,
// numbers, booleans and objects built here, which json.Marshal cannot refuse;
// the one failure it has (an unsupported value) cannot be reached from a chunk,
// so there is no error to return.
func FrameData(o Obj) []byte {
	b, err := json.Marshal(o)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// WithExtra merges unmodelled fields into o without overriding a modelled one.
func WithExtra(o Obj, extra map[string]json.RawMessage) Obj {
	for k, v := range extra {
		if _, taken := o[k]; !taken {
			o[k] = v
		}
	}
	return o
}

func IntPtr(r gjson.Result) *int {
	if !r.Exists() || r.Type != gjson.Number {
		return nil
	}
	n := int(r.Int())
	return &n
}

func FloatPtr(r gjson.Result) *float64 {
	if !r.Exists() || r.Type != gjson.Number {
		return nil
	}
	f := r.Float()
	return &f
}

func Int64Ptr(r gjson.Result) *int64 {
	if !r.Exists() || r.Type != gjson.Number {
		return nil
	}
	n := r.Int()
	return &n
}

func StrList(r gjson.Result) []string {
	switch {
	case r.Type == gjson.String:
		return []string{r.String()}
	case r.IsArray():
		var out []string
		for _, v := range r.Array() {
			if v.Type == gjson.String {
				out = append(out, v.String())
			}
		}
		return out
	}
	return nil
}

func ValidObject(body []byte) bool {
	return gjson.ValidBytes(body) && gjson.ParseBytes(body).IsObject()
}
