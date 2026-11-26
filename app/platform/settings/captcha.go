package settings

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/goravel/framework/contracts/config"
)

// captchaProviders names the challenge a sign-in may be asked to pass.
var captchaProviders = []string{"turnstile", "recaptcha"}

const maxKeyLen = 256

// Captcha is the validated view of the AUTH_CAPTCHA_* settings. A zero value
// serves no challenge.
type Captcha struct {
	Notices

	// Provider is empty when the sign-in page asks no challenge.
	Provider string
	// SiteKey is public: the page hands it to the provider's script.
	SiteKey string
	// Secret verifies an answer. It is never logged or sent to a page.
	Secret string
}

// LoadCaptcha reads and checks the AUTH_CAPTCHA_* settings. A provider named
// without both keys stops the start rather than serving a form that can never
// be answered.
func LoadCaptcha(cfg config.Config) (*Captcha, error) {
	c := &Captcha{
		Provider: strings.ToLower(strings.TrimSpace(cfg.GetString("auth.captcha_provider"))),
		SiteKey:  strings.TrimSpace(cfg.GetString("auth.captcha_site_key")),
		Secret:   strings.TrimSpace(cfg.GetString("auth.captcha_secret")),
	}
	if c.Provider == "" {
		return c, nil
	}
	ok := false
	for _, p := range captchaProviders {
		ok = ok || c.Provider == p
	}
	if !ok {
		return nil, fmt.Errorf("settings: AUTH_CAPTCHA_PROVIDER: must be one of %s", strings.Join(captchaProviders, ", "))
	}
	if err := checkPrint(c.SiteKey, "AUTH_CAPTCHA_SITE_KEY"); err != nil {
		return nil, err
	}
	if err := checkPrint(c.Secret, "AUTH_CAPTCHA_SECRET"); err != nil {
		return nil, err
	}
	if c.SiteKey == "" || c.Secret == "" {
		return nil, errors.New("settings: AUTH_CAPTCHA_PROVIDER: needs AUTH_CAPTCHA_SITE_KEY and AUTH_CAPTCHA_SECRET")
	}
	return c, nil
}

// Enabled reports whether a sign-in is asked to pass a challenge.
func (c *Captcha) Enabled() bool { return c != nil && c.Provider != "" }

// checkPrint refuses a value that is too long, blank or carries a control
// character. The error names the variable only.
func checkPrint(v, env string) error {
	if len(v) > maxKeyLen || strings.ContainsFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return fmt.Errorf("settings: %s: must be at most %d printable characters", env, maxKeyLen)
	}
	return nil
}
