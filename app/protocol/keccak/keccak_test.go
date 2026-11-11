package keccak

import (
	"crypto/sha3"
	"encoding/hex"
	"strings"
	"testing"
)

// The full 24-round hash is SHA3-256; pinning it against the standard library
// proves the permutation, the round constants and the padding together.
func TestSum256WithFirstZeroIsSHA3(t *testing.T) {
	for _, msg := range [][]byte{
		nil,
		[]byte(""),
		[]byte("abc"),
		[]byte("The quick brown fox jumps over the lazy dog"),
		make([]byte, rate-1),
		make([]byte, rate),
		make([]byte, rate+1),
		make([]byte, 3*rate+7),
	} {
		want := sha3.Sum256(msg)
		if got := Sum256(msg, 0); got != want {
			t.Fatalf("%d bytes:\n got %s\nwant %s", len(msg), hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
		}
	}
}

// DeepSeekHashV1 is SHA3-256 with round 0 skipped. These digests come from an
// independent FIPS 202 implementation written for this test, cross-checked
// against hashlib.sha3_256 at first = 0; no library exposes a partial-round
// hash, so the 23-round case has no third party to check against here.
func TestSum256WithFirstOneIsDeepSeekHashV1(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"", "e594808bc5b7151ac160c6d39a02e0a8e261ed588578403099e3561dc40c26b3"},
		{"abc", "f841106c601ce9be9bc38525e90d4178d47f21dd8eb9f238fc55ffaa4ca94506"},
		{"salt_1700000000_42", "48d97ae065f7deeaf76b0a3c5cb8290a5f2d99f26742949c1b543725cb464ba1"},
		{strings.Repeat("y", 300), "6f1b70aeab56470d3c46bdc2d490ebf474207a9c34d67a3455ef566d1cd4c1ee"},
		{"salt_1700000000_" + strings.Repeat("1", 40), "a8b3e99365f7f273916004977c5194fe49a32802201ac7e0ad9fe27aebc1000b"},
	}
	for _, c := range cases {
		got := Sum256([]byte(c.msg), 1)
		if hex.EncodeToString(got[:]) != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.msg[:min(len(c.msg), 24)], hex.EncodeToString(got[:]), c.want)
		}
	}
}

// A first round outside the permutation's range is clamped: 0 runs every round
// and rounds itself away, so both extremes stay usable.
func TestFirstIsClamped(t *testing.T) {
	if Sum256([]byte("x"), -3) != Sum256([]byte("x"), 0) {
		t.Error("a negative first round did not behave like 0")
	}
	if Sum256([]byte("x"), 99) != Sum256([]byte("x"), 24) {
		t.Error("a first round past the end did not behave like 24")
	}
}

func FuzzSum256(f *testing.F) {
	f.Add([]byte(""), 0)
	f.Add([]byte("abc"), 1)
	f.Add(make([]byte, 200), 0)
	f.Fuzz(func(t *testing.T, data []byte, first int) {
		got := Sum256(data, first)
		want := Sum256(append([]byte{}, data...), first)
		if got != want {
			t.Fatal("the same input hashed differently twice")
		}
		if first <= 0 {
			if std := sha3.Sum256(data); got != std {
				t.Fatalf("first=%d differs from SHA3-256", first)
			}
		}
	})
}
