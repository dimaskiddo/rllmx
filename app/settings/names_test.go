package settings

import (
	"slices"
	"strings"
	"testing"
)

func TestKnownNamesFollowTheNamingRules(t *testing.T) {
	seen := map[string]bool{}
	folded := map[string]string{}
	for _, name := range Known() {
		if !namePattern.MatchString(name) {
			t.Errorf("%q does not match the naming pattern", name)
		}
		if seen[name] {
			t.Errorf("%q is declared twice", name)
		}
		seen[name] = true
		if other, ok := folded[strings.ToLower(name)]; ok && other != name {
			t.Errorf("%q and %q collide after case folding", name, other)
		}
		folded[strings.ToLower(name)] = name
	}
}

func TestOwnedGroupsAreUpperCaseAndEndWithAnUnderscore(t *testing.T) {
	for _, g := range ownedGroups {
		if g != strings.ToUpper(g) || !strings.HasSuffix(g, "_") {
			t.Errorf("group %q", g)
		}
	}
}

func TestKnownReturnsACopy(t *testing.T) {
	a := Known()
	a[0] = "CHANGED"
	if slices.Contains(Known(), "CHANGED") {
		t.Fatal("Known exposes the internal list")
	}
}

func TestCheckNames(t *testing.T) {
	if err := CheckNames([]string{"APP_KEY", "JWT_SECRET", "DATA_DIR", "HTTP_SERVER_IDLE_TIMEOUT", "REDIS_HOST", "PATH"}); err != nil {
		t.Fatalf("known names rejected: %v", err)
	}
	for _, bad := range []string{"SERVER_NOPE", "HTTP_SERVER_IDLE", "auth_initial_password", "Stream_Stall_Timeout", "PROVIDER_AZURE_ENDPOINT"} {
		err := CheckNames([]string{bad})
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("%q: error = %v", bad, err)
		}
	}
}
