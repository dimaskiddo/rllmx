package repository

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/dialect"
	"rllmx/app/document"
	"rllmx/app/sealer"
)

// Client keys, provider credentials and the SSO client secret are stored sealed.
// Repositories open them on read and seal on write, keeping the stored
// ciphertext of unchanged secrets. The "rllm" prefix of the additional data and
// the check scope are part of the stored format, as in sealer.
const (
	cryptScope    = "rllmCrypto"
	cryptCheckKey = "check"
	cryptCheckAAD = "rllm/v1/check"
	cryptCheckVal = "rllm-crypto-check"
	apiKeyAAD     = "rllm/v1/apiKeys/key"
)

var connSecretPaths = []string{
	"apiKey", "accessToken", "refreshToken", "idToken", "sessionToken", "copilotToken",
	"providerSpecificData.apiKey", "providerSpecificData.sessionToken", "providerSpecificData.copilotToken",
	"providerSpecificData.accessKeyId", "providerSpecificData.secretAccessKey", "providerSpecificData.mimoPassToken",
	"providerSpecificData.cookie", "providerSpecificData.clientSecret",
}

var settingsSecretPaths = []string{"oidcClientSecret"}

func connAAD(id string) func(string) string {
	return func(path string) string { return "rllm/v1/providerConnections/" + id + "/" + path }
}

func settingsAAD(path string) string { return "rllm/v1/settings/" + path }

// openFields replaces each sealed string at paths with its plain value and
// leaves plain values alone. It reports false when one does not open.
func openFields(box *sealer.Box, doc string, paths []string, aad func(string) string) (string, bool) {
	if !gjson.Valid(doc) {
		return doc, true
	}
	for _, p := range paths {
		r := gjson.Get(doc, p)
		if r.Type != gjson.String || !sealer.IsSealed(r.String()) {
			continue
		}
		plain, err := box.Open(aad(p), r.String())
		if err != nil {
			return doc, false
		}
		next, err := sjson.SetRaw(doc, p, document.Encode(plain))
		if err != nil {
			return doc, false
		}
		doc = next
	}
	return doc, true
}

// sealFields seals each non-empty string at paths. A secret whose stored
// ciphertext still opens to the same value keeps that ciphertext.
func sealFields(box *sealer.Box, plain, stored string, paths []string, aad func(string) string) (string, error) {
	if !gjson.Valid(plain) {
		return plain, nil
	}
	for _, p := range paths {
		r := gjson.Get(plain, p)
		if r.Type != gjson.String || r.String() == "" {
			continue
		}
		sealed := ""
		if old := gjson.Get(stored, p); old.Type == gjson.String && sealer.IsSealed(old.String()) {
			if v, err := box.Open(aad(p), old.String()); err == nil && v == r.String() {
				sealed = old.String()
			}
		}
		if sealed == "" {
			sealed = box.Seal(aad(p), r.String())
		}
		next, err := sjson.SetRaw(plain, p, document.Encode(sealed))
		if err != nil {
			return plain, fmt.Errorf("repository: seal %s: %w", p, err)
		}
		plain = next
	}
	return plain, nil
}

func (s *Store) sealer() (*sealer.Box, error) {
	if s.box == nil {
		return nil, ErrEncryptionSecret
	}
	return s.box, nil
}

// SealAPIKey returns the stored form of a client key. Equal keys give equal
// text, so the key column keeps its unique index and exact-match lookup.
func (s *Store) SealAPIKey(key string) (string, error) {
	box, err := s.sealer()
	if err != nil {
		return "", err
	}
	return box.SealFixed(apiKeyAAD, dialect.StripNUL(key)), nil
}

// OpenAPIKey returns the plain key of a stored value; a value that is not
// sealed is returned as it is.
func (s *Store) OpenAPIKey(stored string) (string, error) {
	if !sealer.IsSealed(stored) {
		return stored, nil
	}
	box, err := s.sealer()
	if err != nil {
		return "", err
	}
	return box.Open(apiKeyAAD, stored)
}

// SealConnectionData returns the data column text for a connection id, with
// every secret field sealed.
func (s *Store) SealConnectionData(id, plain string) (string, error) {
	box, err := s.sealer()
	if err != nil {
		return "", err
	}
	return sealFields(box, plain, "", connSecretPaths, connAAD(id))
}

// OpenConnectionData opens the sealed fields of a connection's data.
func (s *Store) OpenConnectionData(id, stored string) (string, bool) {
	box, err := s.sealer()
	if err != nil {
		return stored, false
	}
	return openFields(box, stored, connSecretPaths, connAAD(id))
}

// SealSettings seals the secret fields of a settings document.
func (s *Store) SealSettings(plain string) (string, error) {
	box, err := s.sealer()
	if err != nil {
		return "", err
	}
	return sealFields(box, plain, "", settingsSecretPaths, settingsAAD)
}
