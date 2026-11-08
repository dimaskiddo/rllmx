package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/goravel/framework/hash"

	"rllmx/tests"
)

func TestKeyNormalisesIPv6ToItsPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"203.0.113.9:5000", "203.0.113.9"},
		{"::ffff:203.0.113.9", "203.0.113.9"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"[2001:db8::1]:443", "2001:db8::/64"},
		{"127.0.0.1:1", "127.0.0.1"},
		{"not an address", "not an address"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Key(tc.in); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTwoAddressesInOneV6PrefixShareABucket(t *testing.T) {
	a := Key("2001:db8:1:2:aaaa::1")
	b := Key("2001:db8:1:2:bbbb::9")
	if a != b {
		t.Fatalf("%q and %q are one host's prefix and must share a lock", a, b)
	}
}

func TestLimiterLocksAfterFiveFreeAttempts(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for i := 1; i < FreeAttempts; i++ {
		left, locked := l.Fail("k", now)
		if locked != 0 || left != FreeAttempts-i {
			t.Fatalf("attempt %d: left=%d locked=%v", i, left, locked)
		}
	}
	_, locked := l.Fail("k", now)
	if locked != lockSteps[0] {
		t.Fatalf("the fifth failure locked %v, want %v", locked, lockSteps[0])
	}
	if got := l.Locked("k", now.Add(locked/2)); got <= 0 {
		t.Fatal("the key is not locked while the first lock runs")
	}
	if got := l.Locked("k", now.Add(locked+time.Second)); got != 0 {
		t.Fatalf("still locked %v after the step passed", got)
	}
}

func TestLimiterEscalatesAndThenRepeatsTheLongestStep(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	var got []time.Duration
	for range len(lockSteps) + 1 {
		for range FreeAttempts {
			_, locked := l.Fail("k", now)
			if locked > 0 {
				got = append(got, locked)
			}
		}
	}
	want := []time.Duration{lockSteps[0], lockSteps[1], lockSteps[2], lockSteps[3], lockSteps[3]}
	if len(got) != len(want) {
		t.Fatalf("locks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("locks = %v, want %v", got, want)
		}
	}
}

func TestAQuietHourResetsTheHistory(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for range FreeAttempts {
		l.Fail("k", now)
	}
	if l.Locked("k", now.Add(time.Second)) == 0 {
		t.Fatal("the key is not locked")
	}
	later := now.Add(quietAfter + time.Minute)
	if got := l.Locked("k", later); got != 0 {
		t.Fatalf("still locked after the quiet hour: %v", got)
	}
	left, locked := l.Fail("k", later)
	if locked != 0 || left != FreeAttempts-1 {
		t.Fatalf("the escalation survived the quiet hour: left=%d locked=%v", left, locked)
	}
}

func TestASuccessWipesTheHistory(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for range FreeAttempts - 1 {
		l.Fail("k", now)
	}
	l.Succeed("k")
	left, locked := l.Fail("k", now)
	if left != FreeAttempts-1 || locked != 0 {
		t.Fatalf("no fresh budget after a success: left=%d locked=%v", left, locked)
	}
}

func TestTheStoreIsCapped(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	for i := range maxEntries + 10 {
		l.Fail(string(rune('a'+i%26))+string(rune(i)), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(l.entries) > maxEntries {
		t.Fatalf("%d entries, cap is %d", len(l.entries), maxEntries)
	}
}

func TestJudgeAppliesTheNewPasswordRules(t *testing.T) {
	cases := []struct {
		name string
		pw   string
		want error
	}{
		{"short", "1234567", ErrPasswordShort},
		{"long", string(make([]byte, 73)), ErrPasswordLong},
		{"default is short first", "123456", ErrPasswordShort},
		{"change-me", "change-me", ErrPasswordDefault},
		{"ok", "a-long-enough-passphrase", nil},
	}
	for _, tc := range cases {
		if got := Judge(tc.pw); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckFallsBackToTheInitialPasswordWhileNoneIsStored(t *testing.T) {
	p := NewPasswords(nil, "")
	if !p.Check("", DefaultPassword) {
		t.Fatal("the default password was refused while no hash is stored")
	}
	if p.Check("", "") {
		t.Fatal("an empty answer passed")
	}
	if p.Check("", "something-else") {
		t.Fatal("any password passed with no hash and no initial value")
	}
	configured := NewPasswords(nil, "a-configured-initial-value")
	if !configured.Check("", "a-configured-initial-value") || configured.Check("", DefaultPassword) {
		t.Fatal("the configured initial password was not the one accepted")
	}
}

func TestWellKnownReportsThePublicDefaults(t *testing.T) {
	p := NewPasswords(nil, "")
	if !p.WellKnown("") {
		t.Fatal("no hash stored is not reported as a known default")
	}
	if p.WellKnown("$2a$10$notarealhash") {
		t.Fatal("an unreadable hash is reported as a known default")
	}
	if configured := NewPasswords(nil, "a-configured-initial-value"); configured.WellKnown("") {
		t.Fatal("a configured initial password is reported as a public default")
	}
}

func TestPVChangesWithTheHashAndTheEpoch(t *testing.T) {
	base := PV(testSecret, "hash-a", "0")
	if base == "" || len(base) != pvLen {
		t.Fatalf("pv %q", base)
	}
	if PV(testSecret, "hash-a", "0") != base {
		t.Fatal("the same inputs produced another pv")
	}
	if PV(testSecret, "hash-b", "0") == base {
		t.Fatal("a new password left the pv alone")
	}
	if PV(testSecret, "hash-a", "1") == base {
		t.Fatal("a new epoch left the pv alone")
	}
	if PV([]byte("another-secret-another-secret-xx"), "hash-a", "0") == base {
		t.Fatal("another secret produced the same pv")
	}
	if !SamePV(base, base) || SamePV(base, base+"x") {
		t.Fatal("SamePV compares wrongly")
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"ci", "my laptop", "a-name-" + strings.Repeat("x", 50)} {
		if !ValidName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", " ", "a\nb", "a\x00b", strings.Repeat("x", 65)} {
		if ValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// bcryptHasher is the framework's own hasher at a low cost, so a test exercises
// the real comparison without an application or a slow round.
func bcryptHasher() *hash.Bcrypt {
	return hash.NewBcrypt(tests.Config{Values: map[string]any{"hashing.bcrypt.rounds": 4}})
}

func TestCheckAgainstAStoredHash(t *testing.T) {
	p := NewPasswords(bcryptHasher(), "")
	hash, err := p.Hash("a-long-enough-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Check(hash, "a-long-enough-passphrase") {
		t.Fatal("the right password was refused")
	}
	if p.Check(hash, "the-wrong-password") {
		t.Fatal("the wrong password passed")
	}
	if p.Check(hash, "") {
		t.Fatal("an empty answer passed against a stored hash")
	}
	if p.WellKnown(hash) {
		t.Fatal("a real password is reported as a public default")
	}
	defaultHash, _ := p.Hash(DefaultPassword)
	if !p.WellKnown(defaultHash) {
		t.Fatal("a hash of the default password is not reported as one")
	}
}

func TestCookieFlags(t *testing.T) {
	c := ClearedCookie()
	if c.Name != CookieName || c.MaxAge >= 0 || !c.HttpOnly || c.SameSite != "lax" || c.Path != "/" {
		t.Fatalf("%+v", c)
	}
}
