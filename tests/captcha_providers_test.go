package tests

import (
	"testing"

	"rllmx/app/access/captcha"
	"rllmx/app/platform/settings"
)

// A provider the settings accept must have a verifier and a page loader, or a
// deploy that names it starts and then refuses every remote sign-in.
func TestEveryAcceptedCaptchaProviderCanBeVerifiedAndLoaded(t *testing.T) {
	for _, p := range settings.CaptchaProviders() {
		if v := captcha.New(p, "secret", nil); v == nil || v.URL == "" {
			t.Errorf("%s: no verifier address", p)
		}
		if captcha.Script(p) == "" {
			t.Errorf("%s: no page loader", p)
		}
	}
}
