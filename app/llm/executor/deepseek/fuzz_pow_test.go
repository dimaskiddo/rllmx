package deepseek

import (
	"encoding/hex"
	"testing"
)

// FuzzParseChallenge feeds the challenge reader any answer. It must never
// panic, and a challenge it accepts is one the solver can run: the one
// algorithm it knows, all four text fields, a 64-digit hex digest and a
// difficulty inside its bound.
func FuzzParseChallenge(f *testing.F) {
	f.Add([]byte(`{"data":{"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		`","salt":"s","signature":"g","target_path":"/p","difficulty":1000,"expire_at":1}}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, ok := parseChallenge(raw)
		if !ok {
			return
		}
		if c.Algorithm != "DeepSeekHashV1" || c.Salt == "" || c.Signature == "" || c.TargetPath == "" {
			t.Fatalf("accepted an incomplete challenge: %+v", c)
		}
		if b, err := hex.DecodeString(c.Challenge); err != nil || len(b) != 32 {
			t.Fatalf("accepted a bad digest %q", c.Challenge)
		}
		if c.Difficulty < 1 || c.Difficulty > maxPoWDifficulty || c.ExpireAt < 0 {
			t.Fatalf("accepted a difficulty outside its bound: %+v", c)
		}
	})
}
