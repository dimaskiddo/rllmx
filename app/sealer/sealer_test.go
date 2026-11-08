package sealer

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The secret and every expected value below come from an independent
// implementation (HKDF-SHA256, HMAC-SHA256 and AES-256-GCM of another library),
// not from this package. They pin the stored format.
func vectorBox(t *testing.T) *Box {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	box, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealFixedMatchesIndependentVector(t *testing.T) {
	got := vectorBox(t).SealFixed("rllm/v1/apiKeys/key", "sk-test-key")
	const want = "enc:v1:1ZeWayEIxDOemS7W1HfMeVJBQxKwrXcs1tFaoLw_pf9M6pEI4M5S"
	if got != want {
		t.Fatalf("SealFixed = %s, want %s", got, want)
	}
}

func TestOpenMatchesIndependentVector(t *testing.T) {
	got, err := vectorBox(t).Open("rllm/v1/settings/oidcClientSecret", "enc:v1:ZGVmZ2hpamtsbW5vQb4eOa03McPZc7nbYuGAA8JIL1eiskB7WeI")
	if err != nil || got != "idp-secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestTheStoredPrefixIsPartOfTheFormat(t *testing.T) {
	if Prefix != "enc:v1:" {
		t.Fatalf("prefix %q: 9router-side tools and every stored value depend on it", Prefix)
	}
}

func TestRoundTripAndRandomNonce(t *testing.T) {
	box := vectorBox(t)
	a, b := box.Seal("x", "same"), box.Seal("x", "same")
	if a == b {
		t.Fatal("Seal must use a fresh nonce each call")
	}
	for _, s := range []string{a, b} {
		if got, err := box.Open("x", s); err != nil || got != "same" {
			t.Fatalf("Open = %q, %v", got, err)
		}
	}
	if got, err := box.Open("x", box.Seal("x", "")); err != nil || got != "" {
		t.Fatalf("empty value: %q, %v", got, err)
	}
}

func TestSealFixedIsDeterministicAndBoundToAAD(t *testing.T) {
	box := vectorBox(t)
	first, second := box.SealFixed("a", "v"), box.SealFixed("a", "v")
	if first != second {
		t.Fatal("equal input must give equal output")
	}
	if box.SealFixed("a", "v") == box.SealFixed("b", "v") {
		t.Fatal("the AAD must change the output")
	}
	if box.SealFixed("a", "v") == box.SealFixed("a", "w") {
		t.Fatal("the value must change the output")
	}
	if got, err := box.Open("a", box.SealFixed("a", "v")); err != nil || got != "v" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestOpenRejects(t *testing.T) {
	box := vectorBox(t)
	good := box.Seal("aad", "value")
	other, _ := New(bytes.Repeat([]byte{7}, 32))
	flip := func(s string) string {
		b := []byte(s)
		b[len(b)-3] ^= 1
		return string(b)
	}
	cases := map[string]struct {
		aad, in string
		box     *Box
		want    error
	}{
		"plain text":    {"aad", "sk-plain", box, ErrNotSealed},
		"empty":         {"aad", "", box, ErrNotSealed},
		"wrong aad":     {"other", good, box, ErrTampered},
		"wrong key":     {"aad", good, other, ErrTampered},
		"flipped tag":   {"aad", flip(good), box, ErrTampered},
		"truncated":     {"aad", Prefix + "AAAA", box, ErrTampered},
		"bad base64":    {"aad", Prefix + "!!!!", box, ErrTampered},
		"prefix only":   {"aad", Prefix, box, ErrTampered},
		"other version": {"aad", "enc:v2:" + strings.TrimPrefix(good, Prefix), box, ErrNotSealed},
	}
	for name, c := range cases {
		if _, err := c.box.Open(c.aad, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestTheValueIsUnpaddedURLSafeBase64(t *testing.T) {
	v := vectorBox(t).Seal("a", "some value that is long enough to need padding in standard base64")
	body := strings.TrimPrefix(v, Prefix)
	if strings.ContainsAny(body, "=+/") {
		t.Fatalf("%q: the format is unpadded base64url", body)
	}
}

func TestIsSealed(t *testing.T) {
	box := vectorBox(t)
	if !IsSealed(box.Seal("a", "v")) || IsSealed("sk-x") || IsSealed("") {
		t.Fatal("IsSealed misjudges")
	}
}

func TestNewRejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("length %d accepted", n)
		}
	}
}

func TestParseSecret(t *testing.T) {
	good := strings.Repeat("0f", 32)
	raw, err := ParseSecret(good)
	if err != nil || len(raw) != 32 || raw[0] != 0x0f {
		t.Fatalf("ParseSecret = %x, %v", raw, err)
	}
	for _, bad := range []string{"", good[:62], good + "00", strings.Repeat("zz", 32)} {
		if _, err := ParseSecret(bad); err == nil {
			t.Errorf("%q accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("the error echoes the secret")
		}
	}
}

func TestTheErrorsNeverContainAValue(t *testing.T) {
	box := vectorBox(t)
	sealed := box.Seal("aad", "the-plain-secret")
	_, err := box.Open("wrong", sealed)
	if err == nil || strings.Contains(err.Error(), "the-plain-secret") || strings.Contains(err.Error(), sealed) {
		t.Fatalf("error = %v", err)
	}
}
