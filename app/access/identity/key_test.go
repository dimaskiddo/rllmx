package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The vectors were computed with Python's hmac/sha256 from the derivation PRD 2
// states, not from this package, so a change in the implementation fails here
// instead of agreeing with itself.
const (
	vectorMachine = "0123456789abcdef"
	vectorKeyID   = "abc123"
	vectorCRC     = "3b4c47b0"
	vectorOther   = "2482d033"
	vectorHash    = "8b1a36175c4e27f3"
)

func TestNewBuildsTheDocumentedShape(t *testing.T) {
	k := NewKeys("")
	key, err := k.New(vectorMachine)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(key, "-")
	if len(parts) != 4 || parts[0] != "sk" || parts[1] != vectorMachine {
		t.Fatalf("key %q", key)
	}
	if len(parts[2]) != keyIDLen || len(parts[3]) != crcLen {
		t.Fatalf("key %q: id %d chars, crc %d", key, len(parts[2]), len(parts[3]))
	}
	for i := 0; i < len(parts[2]); i++ {
		if !strings.ContainsRune(keyIDChars, rune(parts[2][i])) {
			t.Fatalf("key id %q uses a character outside the alphabet", parts[2])
		}
	}
	if got := k.Classify(key); got != ClassOK {
		t.Fatalf("a key it just made classifies as %s", got)
	}
}

func TestNewDrawsAFreshKeyIDEveryTime(t *testing.T) {
	k := NewKeys("")
	seen := map[string]bool{}
	for range 50 {
		key, err := k.New(vectorMachine)
		if err != nil {
			t.Fatal(err)
		}
		if seen[key] {
			t.Fatalf("a key repeated within 50 draws: %q", key)
		}
		seen[key] = true
	}
}

func TestChecksumMatchesAnIndependentVector(t *testing.T) {
	if got := NewKeys("").Parse("sk-" + vectorMachine + "-" + vectorKeyID + "-" + vectorCRC).Class; got != ClassOK {
		t.Fatalf("class = %s, want ok", got)
	}
	if got := NewKeys("another-secret").Parse("sk-" + vectorMachine + "-" + vectorKeyID + "-" + vectorCRC).Class; got != ClassChecksumMismatch {
		t.Fatalf("a key under another secret = %s, want mismatch", got)
	}
	if got := NewKeys("").Parse("sk-" + vectorMachine + "-" + vectorKeyID + "-" + vectorOther).Class; got != ClassChecksumMismatch {
		t.Fatalf("a wrong checksum = %s, want mismatch", got)
	}
}

func TestParseTakesAKeyApart(t *testing.T) {
	p := NewKeys("").Parse("sk-" + vectorMachine + "-" + vectorKeyID + "-" + vectorCRC)
	if p.MachineID != vectorMachine || p.KeyID != vectorKeyID || p.Class != ClassOK {
		t.Fatalf("%+v", p)
	}
}

func TestClassCases(t *testing.T) {
	k := NewKeys("")
	cases := []struct {
		name string
		key  string
		want Class
	}{
		{"empty", "", ClassMalformed},
		{"not sk", "pk-0123456789abcdef-abc123-3b4c47b0", ClassMalformed},
		{"too short", "sk-abc", ClassMalformed},
		{"too long", "sk-" + strings.Repeat("a", 300), ClassMalformed},
		{"space", "sk-0123 456789abcdef-abc123-3b4c47b0", ClassMalformed},
		{"newline", "sk-0123456789abcdef-abc123-3b4c47b0\n", ClassMalformed},
		{"colon", "sk-0123456789abcdef:abc-abc123-3b4c47b0", ClassMalformed},
		{"legacy", "sk-abcdefgh", ClassLegacy},
		{"legacy long", "sk-" + strings.Repeat("z", 40), ClassLegacy},
		{"current", "sk-0123456789abcdef-abc123-" + vectorCRC, ClassOK},
		{"short crc", "sk-0123456789abcdef-abc123-3b4c47b", ClassChecksumMismatch},
		{"empty machine", "sk--abc123-" + vectorCRC, ClassChecksumMismatch},
	}
	for _, tc := range cases {
		if got := k.Classify(tc.key); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestClassNamesAreTheOnesTheListPrints(t *testing.T) {
	for class, want := range map[Class]string{ClassOK: "ok", ClassChecksumMismatch: "mismatch", ClassLegacy: "legacy", ClassMalformed: "malformed"} {
		if got := class.String(); got != want {
			t.Errorf("%d = %q, want %q", class, got, want)
		}
	}
}

func TestTheDefaultSecretIsReported(t *testing.T) {
	if !NewKeys("").DefaultSecret() {
		t.Fatal("an empty secret is not reported as the default")
	}
	if NewKeys("another-secret").DefaultSecret() {
		t.Fatal("a configured secret is reported as the default")
	}
	if !NewMachine("").DefaultSalt() || NewMachine("another-salt").DefaultSalt() {
		t.Fatal("the salt is not reported correctly")
	}
}

func TestMachineHashMatchesAnIndependentVector(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, machineFile), []byte("raw-machine-id-0001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := NewMachine("").Load(dir)
	if !ok || got != vectorHash {
		t.Fatalf("hash = %q ok=%v, want %q", got, ok, vectorHash)
	}
	if other, _ := NewMachine("another-salt").Load(dir); other == got {
		t.Fatal("another salt produced the same id")
	}
}

func TestLoadCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, ok := NewMachine("").Load(dir); ok {
		t.Fatal("a missing file reported an id")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("Load wrote a file; only the server may create it")
	}
}

func TestEnsureCreatesTheFilePrivate(t *testing.T) {
	dir := t.TempDir()
	id, err := NewMachine("").Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != machineLen {
		t.Fatalf("id %q", id)
	}
	path := filepath.Join(dir, machineFile)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o, want 600", mode)
	}
	again, err := NewMachine("").Ensure(dir)
	if err != nil || again != id {
		t.Fatalf("a second call changed the id: %q then %q (%v)", id, again, err)
	}
}

func TestEnsureReplacesAnUnusableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, machineFile)
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMachine("").Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewMachine("").Load(dir); !ok {
		t.Fatal("the unusable file was not replaced with a usable id")
	}
}

func TestReadRawRefusesCharactersTheFormatForbids(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, machineFile)
	if err := os.WriteFile(path, []byte("0123456789abcdef ghi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewMachine("").Load(dir); ok {
		t.Fatal("a file with a space was accepted")
	}
}
