package tests

import (
	"testing"

	"rllmx/app/sealer"
)

// DBSecret and OtherDBSecret are two different 64-hex sealing secrets for
// tests. They are public test values, never used outside tests.
const (
	DBSecret      = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	OtherDBSecret = "1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"
)

// Box is the sealing box for a 64-hex secret.
func Box(t testing.TB, secret string) *sealer.Box {
	t.Helper()
	raw, err := sealer.ParseSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sealer.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
