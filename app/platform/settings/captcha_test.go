package settings

import (
	"maps"
	"strings"
	"testing"

	"rllmx/tests"
)

func captchaValues(vals map[string]any) tests.Config {
	return tests.Config{Values: maps.Clone(vals)}
}

func TestNoCaptchaProviderMeansNoChallenge(t *testing.T) {
	c, err := LoadCaptcha(captchaValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled() {
		t.Fatalf("%+v", c)
	}
}

func TestACaptchaProviderNeedsBothKeys(t *testing.T) {
	for name, vals := range map[string]map[string]any{
		"neither key": {"auth.captcha_provider": "turnstile"},
		"no secret":   {"auth.captcha_provider": "recaptcha", "auth.captcha_site_key": "site"},
		"no site key": {"auth.captcha_provider": "recaptcha", "auth.captcha_secret": "secret"},
	} {
		if _, err := LoadCaptcha(captchaValues(vals)); err == nil || !strings.Contains(err.Error(), "AUTH_CAPTCHA") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestAnUnknownCaptchaProviderIsRefusedByName(t *testing.T) {
	_, err := LoadCaptcha(captchaValues(map[string]any{
		"auth.captcha_provider": "hcaptcha", "auth.captcha_site_key": "s", "auth.captcha_secret": "t",
	}))
	if err == nil || !strings.Contains(err.Error(), "AUTH_CAPTCHA_PROVIDER") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "hcaptcha") {
		t.Fatalf("the error repeats the value: %v", err)
	}
}

func TestACaptchaValueWithAControlCharacterIsRefused(t *testing.T) {
	secret := "a-secret-with-a-tab\tinside"
	_, err := LoadCaptcha(captchaValues(map[string]any{
		"auth.captcha_provider": "turnstile", "auth.captcha_site_key": "s", "auth.captcha_secret": secret,
	}))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err %v", err)
	}
}
