package saml

import (
	"crypto/x509"
	"errors"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/russellhaering/goxmldsig/etreeutils"
)

// The verification failures. Each maps to a fixed login error code, and none
// carries a claim value or library text.
var (
	ErrSignature    = errors.New("saml: the signature did not verify against the pinned certificate")
	ErrStatus       = errors.New("saml: the identity provider did not accept the sign-in")
	ErrWrongIssuer  = errors.New("saml: the response names another issuer")
	ErrNoAudience   = errors.New("saml: the response is not addressed to this service provider")
	ErrDestination  = errors.New("saml: the response names another destination")
	ErrRecipient    = errors.New("saml: the assertion names another recipient")
	ErrNotYetValid  = errors.New("saml: the assertion is not valid yet")
	ErrExpired      = errors.New("saml: the assertion has expired")
	ErrInResponseTo = errors.New("saml: the response does not answer this sign-in")
	ErrNotBearer    = errors.New("saml: the subject confirmation is not a bearer one")
	ErrNoAssertion  = errors.New("saml: the response carries no single assertion")
	ErrReplay       = errors.New("saml: the assertion has already been used")
)

// Skew is how far the identity provider's clock may be off.
const Skew = 60 * time.Second

// Expect is what this sign-in demands of a response.
type Expect struct {
	// Issuer is the identity provider's entity id, pinned in the settings.
	Issuer string
	// Audience is our own entity id.
	Audience string
	// Destination is our ACS URL.
	Destination string
	// RequestID is the id of the AuthnRequest this response answers.
	RequestID string
	// Cert is the identity provider's signing certificate.
	Cert *x509.Certificate
	// Replay is the set of assertion ids already used.
	Replay *Replay
	// Now is the clock; zero means time.Now.
	Now time.Time
}

// Verify proves a response and returns the assertion it proved. Only the
// assertion is ever read for identity: the envelope supplies the status and the
// destination, which can only make the check stricter, and never the subject or
// the attributes.
func Verify(encoded string, exp Expect) (Assertion, error) {
	raw, err := decodeRaw(encoded)
	if err != nil {
		return Assertion{}, err
	}
	return VerifyBytes(raw, exp)
}

// VerifyBytes is Verify over decoded XML, for a caller that already holds the
// bytes.
func VerifyBytes(raw []byte, exp Expect) (Assertion, error) {
	if exp.Cert == nil || exp.Replay == nil || exp.Issuer == "" || exp.Audience == "" || exp.Destination == "" || exp.RequestID == "" {
		return Assertion{}, ErrSignature
	}
	now := exp.Now
	if now.IsZero() {
		now = time.Now()
	}

	proof, err := prove(raw, exp.Cert, now)
	if err != nil {
		return Assertion{}, err
	}
	response, err := decodeResponse(proof.envelope)
	if err != nil {
		return Assertion{}, err
	}
	if response.Status.StatusCode.Value != statusSuccess {
		return Assertion{}, ErrStatus
	}
	if len(proof.assertions) != 1 {
		// None, or more than one, where exactly one was proved: the shape of a
		// wrapping attempt. Refuse rather than choose.
		return Assertion{}, ErrNoAssertion
	}
	a := proof.assertions[0]

	// The identity comes from the verified assertion only.
	if strings.TrimSpace(a.Issuer.Value) != exp.Issuer {
		return Assertion{}, ErrWrongIssuer
	}
	if len(a.Conditions.Audiences) == 0 || !hasAudience(a.Conditions.Audiences, exp.Audience) {
		return Assertion{}, ErrNoAudience
	}
	if d := strings.TrimSpace(response.Destination); d != "" && !sameURL(d, exp.Destination) {
		return Assertion{}, ErrDestination
	}

	confirm, err := bearerConfirmation(a)
	if err != nil {
		return Assertion{}, err
	}
	if r := strings.TrimSpace(confirm.Data.Recipient); r != "" && !sameURL(r, exp.Destination) {
		return Assertion{}, ErrRecipient
	}
	if confirm.Data.InResponseTo != "" && confirm.Data.InResponseTo != exp.RequestID {
		return Assertion{}, ErrInResponseTo
	}
	if response.InResponseTo != "" && response.InResponseTo != exp.RequestID {
		return Assertion{}, ErrInResponseTo
	}

	if err := checkWindow(a.Conditions.NotBefore, a.Conditions.NotOnOrAfter, confirm.Data.NotBefore, confirm.Data.NotOnOrAfter, now); err != nil {
		return Assertion{}, err
	}
	// An assertion cannot outlive the replay set's memory, and a provider that
	// asks it to is either misconfigured or probing: the window is minutes.
	if t, ok := parseTime(a.Conditions.NotOnOrAfter); ok && t.After(now.Add(MaxAssertionLife)) {
		return Assertion{}, ErrExpired
	}

	if strings.TrimSpace(a.ID) == "" {
		return Assertion{}, ErrNoAssertion
	}
	if !exp.Replay.Take(a.ID, latestExpiry(a, now), now) {
		return Assertion{}, ErrReplay
	}
	return a, nil
}

// proof is what the signature check established: the envelope to read status and
// destination from, and the assertions that were proved.
type proof struct {
	envelope   []byte
	assertions []Assertion
}

// prove accepts a response signed as a whole, or one whose assertions are each
// signed. Nothing else counts as proof.
func prove(raw []byte, cert *x509.Certificate, now time.Time) (proof, error) {
	doc, err := etreeNew(raw)
	if err != nil {
		return proof{}, err
	}
	if verifiedRoot, err := validateWith(doc, cert, now); err == nil {
		envelope, err := etreeBytes(verifiedRoot)
		if err != nil {
			return proof{}, ErrSignature
		}
		response, err := decodeResponse(envelope)
		if err != nil {
			return proof{}, err
		}
		return proof{envelope: envelope, assertions: response.Assertions}, nil
	}

	// No signature over the envelope: every assertion must carry its own, and
	// only the ones that verify are returned.
	var verified []Assertion
	for _, rawAssertion := range etreeAssertions(raw) {
		assertionDoc, err := etreeNew(rawAssertion)
		if err != nil {
			continue
		}
		verifiedEl, err := validateWith(assertionDoc, cert, now)
		if err != nil {
			continue
		}
		bytes, err := etreeBytes(verifiedEl)
		if err != nil {
			continue
		}
		a, err := decodeAssertion(bytes)
		if err != nil {
			continue
		}
		verified = append(verified, a)
	}
	if len(verified) == 0 {
		return proof{}, ErrSignature
	}
	return proof{envelope: raw, assertions: verified}, nil
}

// validateWith runs the XML-DSig verifier on one element with the pinned
// certificate and the caller's clock.
func validateWith(el *etree.Element, cert *x509.Certificate, now time.Time) (*etree.Element, error) {
	ctx := &dsig.ValidationContext{
		CertificateStore: &dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{cert}},
		IdAttribute:      "ID",
		Clock:            dsig.NewFakeClockAt(now),
	}
	verified, err := ctx.Validate(el)
	if err != nil {
		return nil, ErrSignature
	}
	return verified, nil
}

// bearerConfirmation returns the one bearer confirmation that carries data.
func bearerConfirmation(a Assertion) (SubjectConfirm, error) {
	for _, c := range a.Subject.Confirmations {
		if c.Method != bearer {
			continue
		}
		if strings.TrimSpace(c.Data.Recipient) == "" && strings.TrimSpace(c.Data.NotOnOrAfter) == "" {
			continue
		}
		return c, nil
	}
	return SubjectConfirm{}, ErrNotBearer
}

// checkWindow applies the assertion's and the confirmation's windows with Skew.
func checkWindow(notBefore, notOnOrAfter, confirmBefore, confirmAfter string, now time.Time) error {
	for _, pair := range [][2]string{{notBefore, notOnOrAfter}, {confirmBefore, confirmAfter}} {
		if t, ok := parseTime(pair[0]); ok && now.Add(Skew).Before(t) {
			return ErrNotYetValid
		}
		if t, ok := parseTime(pair[1]); ok && now.After(t.Add(Skew)) {
			return ErrExpired
		}
	}
	return nil
}

// MaxAssertionLife bounds how long an assertion may be presented: a day, which
// is the longest a replay entry is kept.
const MaxAssertionLife = 24 * time.Hour

// latestExpiry is when the assertion can no longer be presented, capped at
// MaxAssertionLife so a provider that omits the two dates cannot pin a replay
// entry forever.
func latestExpiry(a Assertion, now time.Time) time.Time {
	until := now.Add(MaxAssertionLife)
	for _, raw := range []string{a.Conditions.NotOnOrAfter} {
		if t, ok := parseTime(raw); ok && t.Before(until) {
			until = t
		}
	}
	for _, c := range a.Subject.Confirmations {
		if t, ok := parseTime(c.Data.NotOnOrAfter); ok && t.Before(until) {
			until = t
		}
	}
	return until
}

// parseTime reads a SAML timestamp.
func parseTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05.999Z0700"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// hasAudience reports whether the audience list names ours.
func hasAudience(list []Audience, want string) bool {
	for _, a := range list {
		if strings.TrimSpace(a.Value) == want {
			return true
		}
	}
	return false
}

// sameURL compares two URLs ignoring a trailing slash and the case of the host.
func sameURL(a, b string) bool {
	return strings.TrimSuffix(strings.ToLower(a), "/") == strings.TrimSuffix(strings.ToLower(b), "/")
}

// etreeNew parses XML into the tree the signature verifier works on.
func etreeNew(raw []byte) (*etree.Element, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return nil, ErrParse
	}
	return doc.Root(), nil
}

// etreeBytes serialises a verified element back to XML, so every later read goes
// through encoding/xml on proven bytes.
func etreeBytes(el *etree.Element) ([]byte, error) {
	doc := etree.NewDocument()
	doc.SetRoot(el.Copy())
	return doc.WriteToBytes()
}

// etreeAssertions returns the raw XML of every Assertion element in a verified
// document, so each can be checked for its own signature.
func etreeAssertions(raw []byte) [][]byte {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return nil
	}
	var out [][]byte
	err := etreeutils.NSFindIterate(doc.Root(), nsAssertion, "Assertion", func(_ etreeutils.NSContext, el *etree.Element) error {
		one := etree.NewDocument()
		one.SetRoot(el.Copy())
		if b, err := one.WriteToBytes(); err == nil {
			out = append(out, b)
		}
		return nil
	})
	_ = err
	return out
}
