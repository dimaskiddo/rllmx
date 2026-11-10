package saml

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"net/url"
)

// newRequestID returns a SAML request id. The spec requires an NCName, so it
// starts with a letter and holds only letters, digits and hyphens.
func newRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrParse
	}
	return "rllmx-" + hex.EncodeToString(b), nil
}

// authnRequest is the element sent to the identity provider: unsigned, HTTP
// Redirect binding, no name-id policy so the provider picks the subject it knows.
type authnRequest struct {
	XMLName                     xml.Name `xml:"urn:oasis:names:tc:SAML:2.0:protocol AuthnRequest"`
	ID                          string   `xml:"ID,attr"`
	Version                     string   `xml:"Version,attr"`
	IssueInstant                string   `xml:"IssueInstant,attr"`
	Destination                 string   `xml:"Destination,attr"`
	ProtocolBinding             string   `xml:"ProtocolBinding,attr"`
	AssertionConsumerServiceURL string   `xml:"AssertionConsumerServiceURL,attr"`
	Issuer                      issuerEl `xml:"urn:oasis:names:tc:SAML:2.0:assertion Issuer"`
}

type issuerEl struct {
	Value string `xml:",chardata"`
}

// AuthnRequest builds the redirect the browser follows to the identity provider.
// The request is deflated and base64url-encoded into SAMLRequest, and the request
// id travels in RelayState so the response can be tied back to it.
func AuthnRequest(entryPoint, entityID, acsURL, id, issuedAt string) (string, error) {
	if entryPoint == "" || entityID == "" || acsURL == "" || id == "" {
		return "", ErrConfig
	}
	req := authnRequest{
		ID:                          id,
		Version:                     "2.0",
		IssueInstant:                issuedAt,
		Destination:                 entryPoint,
		ProtocolBinding:             "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST",
		AssertionConsumerServiceURL: acsURL,
		Issuer:                      issuerEl{Value: entityID},
	}
	raw, err := xml.Marshal(req)
	if err != nil {
		return "", ErrConfig
	}
	raw = append([]byte(xml.Header), raw...)

	var deflated bytes.Buffer
	w, err := flate.NewWriter(&deflated, flate.BestCompression)
	if err != nil {
		return "", ErrConfig
	}
	if _, err := w.Write(raw); err != nil {
		return "", ErrConfig
	}
	if err := w.Close(); err != nil {
		return "", ErrConfig
	}

	q := url.Values{}
	q.Set("SAMLRequest", base64.StdEncoding.EncodeToString(deflated.Bytes()))
	q.Set("RelayState", id)

	target, err := url.Parse(entryPoint)
	if err != nil {
		return "", ErrConfig
	}
	existing := target.Query()
	for k, v := range q {
		existing[k] = v
	}
	target.RawQuery = existing.Encode()
	return target.String(), nil
}

// entityDocument is the metadata an identity provider is configured from.
type entityDocument struct {
	XMLName  xml.Name     `xml:"urn:oasis:names:tc:SAML:2.0:metadata EntityDescriptor"`
	EntityID string       `xml:"entityID,attr"`
	SP       spDescriptor `xml:"urn:oasis:names:tc:SAML:2.0:metadata SPSSODescriptor"`
}

type spDescriptor struct {
	ProtocolSupport string    `xml:"protocolSupportEnumeration,attr"`
	WantSigned      string    `xml:"WantAssertionsSigned,attr"`
	SignedRequests  string    `xml:"AuthnRequestsSigned,attr"`
	NameIDFormat    nameIDFmt `xml:"urn:oasis:names:tc:SAML:2.0:metadata NameIDFormat"`
	ACS             []acsEl   `xml:"urn:oasis:names:tc:SAML:2.0:metadata AssertionConsumerService"`
}

type nameIDFmt struct {
	Value string `xml:",chardata"`
}

type acsEl struct {
	Binding  string `xml:"Binding,attr"`
	Location string `xml:"Location,attr"`
	Index    int    `xml:"index,attr"`
	Default  string `xml:"isDefault,attr"`
}

// Metadata is the service-provider document: one POST ACS, assertions required
// signed, requests not signed, because this build sends nothing signed.
func Metadata(entityID, acsURL string) ([]byte, error) {
	if entityID == "" || acsURL == "" {
		return nil, ErrConfig
	}
	doc := entityDocument{
		EntityID: entityID,
		SP: spDescriptor{
			ProtocolSupport: "urn:oasis:names:tc:SAML:2.0:protocol",
			WantSigned:      "true",
			SignedRequests:  "false",
			NameIDFormat:    nameIDFmt{Value: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"},
			ACS: []acsEl{{
				Binding:  "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST",
				Location: acsURL,
				Index:    0,
				Default:  "true",
			}},
		},
	}
	raw, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, ErrConfig
	}
	out := append([]byte(xml.Header), raw...)
	return append(out, '\n'), nil
}

// NewRequestID returns an id for an AuthnRequest: an NCName, which is what both
// the spec and the identity provider's `InResponseTo` expect.
func NewRequestID() (string, error) { return newRequestID() }
