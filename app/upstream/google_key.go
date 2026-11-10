package upstream

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Kinds of key a Vertex account can hold.
const (
	KeyRaw            = "raw"
	KeyServiceAccount = "service_account"
	KeyADC            = "authorized_user"
)

// maxGoogleKey bounds the JSON text of a service account or ADC key.
const maxGoogleKey = 16 << 10

// GoogleKey is a Vertex key read for what it is: a service-account file, a
// user's application-default credentials, or a plain API key. The secret parts
// stay in the value and are never printed.
type GoogleKey struct {
	Kind string
	// Project is the project the key belongs to, when the key names one.
	Project string
	// ClientEmail and PrivateKey belong to a service account.
	ClientEmail string
	PrivateKey  string
	// ClientID, ClientSecret and RefreshToken belong to application-default
	// credentials.
	ClientID, ClientSecret, RefreshToken string
}

var (
	projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	location  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
)

// ValidProject reports a Google Cloud project id fit for a request path.
func ValidProject(s string) bool { return projectID.MatchString(s) }

// ValidLocation reports a Vertex location (a region or "global").
func ValidLocation(s string) bool { return location.MatchString(s) }

// ParseGoogleKey reads a key's text. Text that is not a JSON object of a known
// type is a raw API key. A service account without its email, key or project, and
// credentials without their client or token, are not keys of that kind: ok is
// false for them, so a half-written file is refused instead of sent as a key.
func ParseGoogleKey(text string) (k GoogleKey, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxGoogleKey {
		return GoogleKey{}, false
	}
	if !strings.HasPrefix(text, "{") {
		return GoogleKey{Kind: KeyRaw}, true
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
		return GoogleKey{}, false
	}
	switch f.Type {
	case KeyServiceAccount:
		if f.ClientEmail == "" || f.PrivateKey == "" || !ValidProject(f.ProjectID) {
			return GoogleKey{}, false
		}
		return GoogleKey{Kind: KeyServiceAccount, Project: f.ProjectID, ClientEmail: f.ClientEmail, PrivateKey: f.PrivateKey}, true
	case KeyADC:
		if f.ClientID == "" || f.ClientSecret == "" || f.RefreshToken == "" {
			return GoogleKey{}, false
		}
		k := GoogleKey{Kind: KeyADC, ClientID: f.ClientID, ClientSecret: f.ClientSecret, RefreshToken: f.RefreshToken}
		if ValidProject(f.QuotaProject) {
			k.Project = f.QuotaProject
		}
		return k, true
	}
	return GoogleKey{}, false
}
