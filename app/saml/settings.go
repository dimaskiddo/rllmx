// Package saml is the service-provider side of SAML 2.0 sign-in: the request it
// sends, the response it verifies, and the metadata an identity provider is
// configured from.
package saml

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"

	"rllmx/app/document"
	"rllmx/app/network"
)

// ACSBodyLimit is how large a POST binding response may be. A real assertion
// with a certificate chain is a few kilobytes; 512 KiB is room to spare.
const ACSBodyLimit = 512 << 10

// ErrConfig is returned for a settings value this program cannot use. It names
// no value.
var ErrConfig = errors.New("saml: the identity-provider settings are incomplete or malformed")

// Settings is what the settings row holds for SAML.
type Settings struct {
	EntryPoint     string
	EntityID       string
	Cert           string
	LoginLabel     string
	AttributeEmail string
	AttributeName  string
	// IdPEntityID pins the issuer the response must name. It is not a
	// 9router setting, and an imported database leaves it empty.
	IdPEntityID string
}

// FromDoc reads the SAML settings from the settings row.
func FromDoc(d document.Doc) Settings {
	s := Settings{}
	if v, ok := d.GetString("samlEntryPoint"); ok {
		s.EntryPoint = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("samlIssuer"); ok {
		s.EntityID = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("samlCert"); ok {
		s.Cert = v
	}
	if v, ok := d.GetString("samlLoginLabel"); ok {
		s.LoginLabel = v
	}
	if v, ok := d.GetString("samlAttributeEmail"); ok {
		s.AttributeEmail = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("samlAttributeName"); ok {
		s.AttributeName = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("samlIdpEntityId"); ok {
		s.IdPEntityID = strings.TrimSpace(v)
	}
	return s
}

// Configured reports whether a sign-in is possible: an entry point and a
// certificate. The entity id is derived when the setting is empty, so it never
// blocks a fresh install.
func (s Settings) Configured() bool {
	return s.EntryPoint != "" && s.Cert != ""
}

// EntityIDFor returns our service-provider entity id: the stored override, else
// the metadata URL derived from APP_URL, which is what an IdP is pointed at.
func (s Settings) EntityIDFor(base string) string {
	if s.EntityID != "" {
		return s.EntityID
	}
	return strings.TrimSuffix(base, "/") + "/api/auth/saml/metadata"
}

// Validate checks the entry point, the certificate and the entity-id override.
func (s Settings) Validate() (string, error) {
	entry, err := ParseEntryPoint(s.EntryPoint)
	if err != nil {
		return "", err
	}
	if _, err := ParseCertificate(s.Cert); err != nil {
		return "", err
	}
	if s.EntityID != "" && !validEntityID(s.EntityID) {
		return "", ErrConfig
	}
	return entry, nil
}

// ParseEntryPoint returns the IdP's single sign-on URL: https, or http on this
// machine, with a host and no credentials.
func ParseEntryPoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrConfig
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return "", ErrConfig
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && network.IsLoopbackHost(u.Hostname())) {
		return "", ErrConfig
	}
	return raw, nil
}

// validEntityID accepts an https URL or a urn: value. Those are the two spellings
// every identity provider uses for an entity id.
func validEntityID(id string) bool {
	if strings.HasPrefix(strings.ToLower(id), "urn:") && len(id) > 4 {
		return true
	}
	u, err := url.Parse(id)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

// parsedCert is a certificate with the stored text it came from.
type parsedCert struct {
	raw  string
	cert *x509.Certificate
}

// lastCert keeps the latest parsed certificate, because the ACS parses it on
// every sign-in and the value only changes when the row does. The pair is
// never edited once stored, so a reader needs no lock.
var lastCert atomic.Pointer[parsedCert]

// ParseCertificate reads the IdP signing certificate. It accepts a PEM block or
// bare base64, which is what a provider's metadata page hands out.
func ParseCertificate(raw string) (*x509.Certificate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrConfig
	}
	if p := lastCert.Load(); p != nil && p.raw == raw {
		return p.cert, nil
	}

	der, err := certificateDER(raw)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrConfig
	}
	lastCert.Store(&parsedCert{raw: raw, cert: cert})
	return cert, nil
}

// certificateDER turns either spelling into DER.
func certificateDER(raw string) ([]byte, error) {
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, ErrConfig
		}
		return block.Bytes, nil
	}
	compact := strings.Join(strings.Fields(raw), "")
	der, err := base64.StdEncoding.DecodeString(compact)
	if err != nil || len(der) == 0 {
		return nil, ErrConfig
	}
	return der, nil
}
