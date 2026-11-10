package identity

import (
	"strings"
	"testing"
)

func TestAnonymousLocalIsStableAndNotAKey(t *testing.T) {
	a, b := AnonymousLocal(), AnonymousLocal()
	if a.ID != b.ID || a.ID == "" {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	if !a.Anonymous || a.Restricted || len(a.Allow) != 0 {
		t.Fatalf("%+v", a)
	}
	if strings.HasPrefix(a.ID, "sk-") {
		t.Fatal("the anonymous id must never look like a key")
	}
}

func TestKeyStatusZeroValueIsNone(t *testing.T) {
	var r KeyResult
	if r.Status != KeyNone {
		t.Fatal("the zero result must mean no key")
	}
	if KeyNone == KeyValid || KeyInvalid == KeyValid || KeyNone == KeyInvalid {
		t.Fatal("statuses must differ")
	}
}
