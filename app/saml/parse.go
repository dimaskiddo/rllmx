package saml

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// MaxXMLBytes caps a decoded response. A real assertion with a certificate chain
// is a few kilobytes; the cap is what stops a base64 bomb before the parser sees
// it.
const MaxXMLBytes = 512 << 10

// The SAML status value that means the identity provider accepted the sign-in.
const statusSuccess = "urn:oasis:names:tc:SAML:2.0:status:Success"

// bearer is the subject-confirmation method for a browser POST.
const bearer = "urn:oasis:names:tc:SAML:2.0:cm:bearer"

// nsAssertion is the SAML assertion namespace, which every element read here
// lives in.
const nsAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"

// ErrParse is returned for a response this program cannot read. It names no
// value.
var ErrParse = errors.New("saml: the response is not a readable SAML document")

// Response is a decoded SAML response, reduced to what this program uses.
type Response struct {
	XMLName      xml.Name    `xml:"Response"`
	ID           string      `xml:"ID,attr"`
	InResponseTo string      `xml:"InResponseTo,attr"`
	Destination  string      `xml:"Destination,attr"`
	IssueInstant string      `xml:"IssueInstant,attr"`
	Issuer       Issuer      `xml:"Issuer"`
	Status       Status      `xml:"Status"`
	Assertions   []Assertion `xml:"Assertion"`
}

// Issuer names the identity provider.
type Issuer struct {
	Value string `xml:",chardata"`
}

// Status is the response's result.
type Status struct {
	StatusCode StatusCode `xml:"StatusCode"`
}

// StatusCode carries the result value.
type StatusCode struct {
	Value string `xml:"Value,attr"`
}

// Assertion is what a sign-in claims.
type Assertion struct {
	XMLName         xml.Name         `xml:"Assertion"`
	ID              string           `xml:"ID,attr"`
	IssueInstant    string           `xml:"IssueInstant,attr"`
	Issuer          Issuer           `xml:"Issuer"`
	Subject         Subject          `xml:"Subject"`
	Conditions      Conditions       `xml:"Conditions"`
	AuthnStatements []AuthnStatement `xml:"AuthnStatement"`
	Attributes      []Attribute      `xml:"AttributeStatement>Attribute"`
}

// Subject identifies who signed in.
type Subject struct {
	NameID        NameID           `xml:"NameID"`
	Confirmations []SubjectConfirm `xml:"SubjectConfirmation"`
}

// NameID is the subject identifier.
type NameID struct {
	Format string `xml:"Format,attr"`
	Value  string `xml:",chardata"`
}

// SubjectConfirm binds the assertion to one use.
type SubjectConfirm struct {
	Method string      `xml:"Method,attr"`
	Data   SubjectData `xml:"SubjectConfirmationData"`
}

// SubjectData is where and when the assertion may be presented.
type SubjectData struct {
	InResponseTo string `xml:"InResponseTo,attr"`
	Recipient    string `xml:"Recipient,attr"`
	NotOnOrAfter string `xml:"NotOnOrAfter,attr"`
	NotBefore    string `xml:"NotBefore,attr"`
}

// Conditions bound the assertion's validity.
type Conditions struct {
	NotBefore    string     `xml:"NotBefore,attr"`
	NotOnOrAfter string     `xml:"NotOnOrAfter,attr"`
	Audiences    []Audience `xml:"AudienceRestriction>Audience"`
}

// Audience names a service provider the assertion is for.
type Audience struct {
	Value string `xml:",chardata"`
}

// AuthnStatement records how the subject authenticated.
type AuthnStatement struct {
	AuthnInstant string `xml:"AuthnInstant,attr"`
	SessionIndex string `xml:"SessionIndex,attr"`
}

// Attribute is one claim the identity provider asserts.
type Attribute struct {
	Name   string   `xml:"Name,attr"`
	Values []string `xml:"AttributeValue"`
}

// Value returns the first value of an attribute, trimmed, or "".
func (a Attribute) Value() string {
	if len(a.Values) == 0 {
		return ""
	}
	return strings.TrimSpace(a.Values[0])
}

// decodeRaw turns the form field into XML bytes: base64, size capped, and a DTD
// refused before any parser sees it, because an entity expansion is a memory
// bomb and this input is entirely attacker-supplied.
func decodeRaw(encoded string) ([]byte, error) {
	compact := strings.Join(strings.Fields(encoded), "")
	if compact == "" {
		return nil, ErrParse
	}
	raw, err := base64.StdEncoding.DecodeString(compact)
	if err != nil || len(raw) == 0 || len(raw) > MaxXMLBytes {
		return nil, ErrParse
	}
	lower := bytes.ToLower(raw)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return nil, ErrParse
	}
	return raw, nil
}

// decodeResponse reads verified bytes as a Response. It is only ever called on
// bytes the XML-DSig verifier has just proved.
func decodeResponse(raw []byte) (Response, error) {
	var r Response
	if err := decodeOne(raw, &r); err != nil {
		return Response{}, err
	}
	return r, nil
}

// decodeAssertion reads verified bytes as an Assertion.
func decodeAssertion(raw []byte) (Assertion, error) {
	var a Assertion
	if err := decodeOne(raw, &a); err != nil {
		return Assertion{}, err
	}
	return a, nil
}

// decodeOne refuses a document with more than one root, which is what a parser
// that stops at the first close would hide.
func decodeOne(raw []byte, into any) error {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	if err := dec.Decode(into); err != nil {
		return ErrParse
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return ErrParse
	}
	return nil
}
