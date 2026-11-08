package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
)

// canonicalBody rewrites a JSON object so two requests that mean the same thing
// have the same bytes: the top-level "stream" is removed, object keys are
// sorted at every depth, and numbers keep the text they were sent with. It
// refuses (false) a body that is not one JSON object, or that repeats a key at
// any depth: the codecs read the first of two equal keys and encoding/json the
// last, so such a body has no single meaning to key on.
func canonicalBody(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out bytes.Buffer
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	if !writeObject(dec, &out, true) {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return out.Bytes(), true
}

// writeObject copies the members of an object whose opening brace was read.
func writeObject(dec *json.Decoder, out *bytes.Buffer, top bool) bool {
	type member struct {
		key string
		val []byte
	}
	var members []member
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		key, ok := kt.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		var val bytes.Buffer
		if !writeValue(dec, &val) {
			return false
		}
		if top && key == "stream" {
			continue
		}
		members = append(members, member{key, val.Bytes()})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return false
	}
	sort.Slice(members, func(i, j int) bool { return members[i].key < members[j].key })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		out.Write(k)
		out.WriteByte(':')
		out.Write(m.val)
	}
	out.WriteByte('}')
	return true
}

func writeValue(dec *json.Decoder, out *bytes.Buffer) bool {
	t, err := dec.Token()
	if err != nil {
		return false
	}
	switch v := t.(type) {
	case json.Delim:
		if v == '{' {
			return writeObject(dec, out, false)
		}
		out.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				out.WriteByte(',')
			}
			first = false
			if !writeValue(dec, out) {
				return false
			}
		}
		if e, err := dec.Token(); err != nil || e != json.Delim(']') {
			return false
		}
		out.WriteByte(']')
	case json.Number:
		out.WriteString(v.String())
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		out.Write(b)
	}
	return true
}

// cacheKey hashes the parts of a request that decide its answer. Each part is
// length-prefixed, so moving bytes from one part to the next changes the hash.
func cacheKey(parts ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
