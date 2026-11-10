package upstream

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Kinds of JSON credential a cloud account can hold.
const (
	// KeyRaw is an opaque key that names no account: it is sent as it is.
	KeyRaw = "raw"
	// KeyServiceAccount is a service account's own key file.
	KeyServiceAccount = "service_account"
	// KeyADC is a user's own default credentials, the file a workstation's
	// tooling already holds.
	KeyADC = "authorized_user"
)

// maxCloudKey bounds the JSON text of a structured credential.
const maxCloudKey = 16 << 10

// CloudKey is a structured credential read for what it is rather than who issues
// it. Its fields mean the same thing at every provider that hands one out: an
// account that names the holder, a project or tenant it belongs to, and the parts
// a token is minted from. The secret parts stay in the value and are never
// printed.
type CloudKey struct {
	Kind string
	// Account is the address that names the credential's holder, when it has one.
	Account string
	// Project is the project, tenant or subscription the credential belongs to,
	// when it names one.
	Project string
	// PrivateKey is the key a token is signed with, for a credential that signs.
	PrivateKey string
	// ClientID, ClientSecret and RefreshToken trade for a token with a grant.
	ClientID, ClientSecret, RefreshToken string
}

var (
	// projectID is the shape of a cloud project identifier: lower case, starting
	// with a letter, digits and dashes inside.
	projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	// location is the shape of a region or zone name, and accepts "global".
	location = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	// region is the shape of a region name, e.g. "us-east-1". It lands in a
	// hostname, so anything else is refused rather than resolved.
	region = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
)

// ValidRegion reports a region name fit to be part of a hostname.
func ValidRegion(s string) bool { return region.MatchString(s) }

// ValidProject reports a project identifier fit for a request path.
func ValidProject(s string) bool { return projectID.MatchString(s) }

// ValidLocation reports a location fit for a request path: a region, a zone or
// the word "global".
func ValidLocation(s string) bool { return location.MatchString(s) }

// ParseCloudKey reads a credential's text. Text that is not a JSON object of a
// known kind is an opaque key, sent as it is. A credential of a known kind that
// is missing what its kind needs is refused, so a half-written file is never
// sent as a key.
func ParseCloudKey(text string) (k CloudKey, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxCloudKey {
		return CloudKey{}, false
	}
	if !strings.HasPrefix(text, "{") {
		return CloudKey{Kind: KeyRaw}, true
	}
	var f struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		QuotaProject string `json:"quota_project_id"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal([]byte(text), &f) != nil {
		return CloudKey{}, false
	}
	switch f.Type {
	case KeyServiceAccount:
		if f.ClientEmail == "" || f.PrivateKey == "" || !ValidProject(f.ProjectID) {
			return CloudKey{}, false
		}
		return CloudKey{Kind: KeyServiceAccount, Project: f.ProjectID, Account: f.ClientEmail, PrivateKey: f.PrivateKey}, true
	case KeyADC:
		if f.ClientID == "" || f.ClientSecret == "" || f.RefreshToken == "" {
			return CloudKey{}, false
		}
		k := CloudKey{Kind: KeyADC, ClientID: f.ClientID, ClientSecret: f.ClientSecret, RefreshToken: f.RefreshToken}
		if ValidProject(f.QuotaProject) {
			k.Project = f.QuotaProject
		}
		return k, true
	}
	return CloudKey{}, false
}
