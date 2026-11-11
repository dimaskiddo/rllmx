package upstream

import "testing"

// The DashScope-family gateways keep a Claude client's prompt-cache markers:
// their data says so, and nothing else may claim the quirk by accident.
func TestTheDashScopeFamilyKeepsCacheMarkers(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alicode", "alicode-intl", "alims-intl", "alitp-intl"} {
		e, ok := reg.ByID(id)
		if !ok {
			t.Fatalf("%s is missing", id)
		}
		if !e.Transport.Quirks.PreserveCacheControl {
			t.Errorf("%s does not keep cache markers", id)
		}
	}
}
