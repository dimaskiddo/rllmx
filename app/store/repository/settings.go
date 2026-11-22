package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// SettingsRepo reads and writes the settings row.
type SettingsRepo struct{ s *Store }

// Settings returns the settings repository.
func (s *Store) Settings() *SettingsRepo { return &SettingsRepo{s} }

// OnSettingsChange registers fn to run after every committed write to the
// settings row made through this store, so caches built from it can be dropped.
func (s *Store) OnSettingsChange(fn func()) {
	s.hookMu.Lock()
	s.onSettings = append(s.onSettings, fn)
	s.hookMu.Unlock()
}

func (s *Store) settingsChanged() {
	s.hookMu.RLock()
	hooks := s.onSettings
	s.hookMu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

var protectedSettings = map[string]bool{"password": true, "mitmSudoEncrypted": true}

// IsSecretSetting reports a settings key that holds a credential or a hash: one
// with its own writer, or one stored sealed. Anything that copies a setting into
// text a client or an upstream can read must refuse these.
func IsSecretSetting(key string) bool {
	return protectedSettings[key] || slices.Contains(settingsSecretPaths, key)
}

type settingDefault struct{ key, raw string }

// settingDefaults are merged in on read and never stored. The values and the
// key set are the ones a 9router settings row is read with; the one key it has
// no counterpart for is samlIdpEntityId, which stays empty so SAML is off
// until an operator sets it.
var settingDefaults = []settingDefault{
	{"cloudEnabled", "false"}, {"tunnelEnabled", "false"}, {"tunnelUrl", `""`}, {"tunnelProvider", `"cloudflare"`},
	{"tailscaleEnabled", "false"}, {"tailscaleUrl", `""`}, {"stickyRoundRobinLimit", "3"}, {"providerStrategies", "{}"},
	{"quotaVisibility", "{}"}, {"comboStrategy", `"fallback"`}, {"comboStickyRoundRobinLimit", "1"}, {"comboStrategies", "{}"},
	{"capacityAdapter", `{"vision":{"enabled":true,"roundRobin":false,"models":[]},"pdf":{"enabled":false,"roundRobin":false,"models":[]},"audioInput":{"enabled":true,"roundRobin":false,"models":[]},"videoInput":{"enabled":false,"roundRobin":false,"models":[]}}`},
	{"requireLogin", "true"}, {"requireApiKey", "true"}, {"tunnelDashboardAccess", "true"}, {"authMode", `"password"`},
	{"ssoType", `"oidc"`}, {"oidcIssuerUrl", `""`}, {"oidcClientId", `""`}, {"oidcClientSecret", `""`},
	{"oidcScopes", `"openid profile email"`}, {"oidcLoginLabel", `"Sign in with OIDC"`}, {"samlEntryPoint", `""`},
	{"samlIssuer", `""`}, {"samlCert", `""`}, {"samlIdpEntityId", `""`}, {"samlLoginLabel", `"Sign in with SAML SSO"`},
	{"samlAttributeEmail", `"email"`}, {"samlAttributeName", `"name"`}, {"enableObservability", "false"},
	{"observabilityMaxRecords", "1000"}, {"observabilityBatchSize", "20"}, {"observabilityFlushIntervalMs", "5000"},
	{"observabilityMaxJsonSize", "5"},
	{"mitmRouterBaseUrl", `"http://localhost:20128"`}, {"dnsToolEnabled", "{}"}, {"rtkEnabled", "true"},
	{"headroomEnabled", "false"}, {"headroomUrl", `""`}, {"headroomCompressUserMessages", "false"},
	{"headroomTimeoutMs", "3000"}, {"cavemanEnabled", "false"}, {"cavemanLevel", `"full"`}, {"cavemanRoutingMode", `"always"`},
	{"ponytailEnabled", "false"}, {"ponytailLevel", `"full"`}, {"ponytailRoutingMode", `"smart"`}, {"customPromptEnabled", "false"}, {"customPrompt", `""`}, {"pxpipeEnabled", "false"},
	{"pxpipeAutoInstall", "true"}, {"pxpipeMinChars", "25000"}, {"pxpipeTimeoutMs", "15000"}, {"providerOverrides", "{}"},
	{"headroomAutoStart", "true"}, {"hermesServiceAutoStart", "true"},
	{"skillSources", "[]"}, {"ecc_auto_skill_routerEnabled", "false"}, {"ecc_auto_skill_routerConfidence", "0.35"},
	{"ecc_auto_skill_routerMaxSkills", "1"},
	{"responseCache", `{"enabled":false,"ttlSeconds":3600,"maxEntryKb":512}`},
	{"claudeAutoPing", "false"}, {"codexAutoPing", "false"},
}

const (
	mimoOld = `"oc/mimo-v2.5-free"`
	mimoNew = `"oc/mimo-v2.6-flash-free"`
)

// readStored returns the settings with the sealed fields opened, and the
// stored text, so a write in the same transaction can reuse the ciphertext of
// secrets that did not change.
func (s *Store) readStored(x contractsdb.Tx) (document.Doc, string, error) {
	var raw []string
	if err := s.sel(x, &raw, `SELECT "data" FROM "settings" WHERE "id" = 1`); err != nil {
		return document.Doc{}, "", err
	}
	if len(raw) == 0 {
		d, err := document.ParseDoc("{}")
		return d, "", err
	}
	d, err := s.openSettings(raw[0])
	return d, raw[0], err
}

// openSettings opens the sealed fields. A document that does not parse comes
// back as its stored text with document.ErrCorruptRow.
func (s *Store) openSettings(raw string) (document.Doc, error) {
	box, err := s.sealer()
	if err != nil {
		return document.Doc{}, err
	}
	if _, perr := document.ParseDoc(raw); perr != nil {
		return document.ParseDoc(raw)
	}
	plain, ok := openFields(box, raw, settingsSecretPaths, settingsAAD)
	if !ok {
		return document.From(raw), fmt.Errorf("%w: a sealed setting does not open", document.ErrCorruptRow)
	}
	return document.ParseDoc(plain)
}

// writeSettings stores the settings document with its secrets sealed. stored
// is the text read in the same transaction.
func (r *SettingsRepo) writeSettings(tx contractsdb.Tx, d document.Doc, stored string) error {
	box, err := r.s.sealer()
	if err != nil {
		return err
	}
	sealed, err := sealFields(box, dialect.StripNUL(d.String()), stored, settingsSecretPaths, settingsAAD)
	if err != nil {
		return err
	}
	return r.s.stmt(tx, `INSERT INTO "settings"("id","data") VALUES(1,?) ON CONFLICT("id") DO UPDATE SET "data"=excluded."data"`, sealed)
}

// Get returns the stored settings exactly as written; no defaults are merged.
func (r *SettingsRepo) Get(ctx context.Context) (document.Doc, error) {
	d, _, err := r.s.readStored(r.s.on(ctx))
	if err != nil {
		return d, fmt.Errorf("settings: %w", err)
	}
	return d, nil
}

// Effective returns the stored settings with defaults filled in. Nothing is persisted.
func (r *SettingsRepo) Effective(ctx context.Context) (document.Doc, error) {
	d, err := r.Get(ctx)
	if err != nil {
		return d, err
	}
	for _, def := range settingDefaults {
		if d.Has(def.key) {
			continue
		}
		if err := d.SetRaw(def.key, def.raw); err != nil {
			return d, err
		}
	}
	if ca, ok := d.GetRaw("capacityAdapter"); ok && strings.Contains(ca, mimoOld) && gjson.Parse(ca).IsObject() {
		if err := d.SetRaw("capacityAdapter", strings.ReplaceAll(ca, mimoOld, mimoNew)); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Patch shallow-merges top-level keys into the stored settings; a null value is stored as null.
func (r *SettingsRepo) Patch(ctx context.Context, patch map[string]string) error {
	for k := range patch {
		if protectedSettings[k] {
			return fmt.Errorf("%w: %s", ErrProtectedSetting, k)
		}
	}
	return r.patch(ctx, patch)
}

// MergeObjectField sets one field of an object-valued setting, or deletes it when raw is null,
// in one transaction. Other fields and settings stay; a non-object stored value is replaced.
func (r *SettingsRepo) MergeObjectField(ctx context.Context, key, field, raw string) error {
	if protectedSettings[key] {
		return fmt.Errorf("%w: %s", ErrProtectedSetting, key)
	}
	if key == "" || field == "" || !gjson.Valid(raw) {
		return fmt.Errorf("repository: settings field %q of %q is not a key with a valid JSON value", field, key)
	}
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		d, stored, err := r.s.readStored(tx)
		if err != nil {
			return fmt.Errorf("settings: %w", err)
		}
		var set map[string]string
		var remove []string
		if gjson.Parse(raw).Type == gjson.Null {
			remove = []string{field}
		} else {
			set = map[string]string{field: raw}
		}
		obj, err := patchObject(d, key, set, remove)
		if err != nil {
			return err
		}
		if err := d.SetRaw(key, obj); err != nil {
			return err
		}
		return r.writeSettings(tx, d, stored)
	})
	if err == nil {
		r.s.settingsChanged()
	}
	return err
}

// RenameObjectField moves one field of an object-valued setting to a new name in
// one transaction, keeping its value. A setting without the old field is left
// alone.
func (r *SettingsRepo) RenameObjectField(ctx context.Context, key, from, to string) error {
	if protectedSettings[key] {
		return fmt.Errorf("%w: %s", ErrProtectedSetting, key)
	}
	if key == "" || from == "" || to == "" {
		return fmt.Errorf("repository: a settings rename needs a key and two field names")
	}
	if from == to {
		return nil
	}
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		d, stored, err := r.s.readStored(tx)
		if err != nil {
			return fmt.Errorf("settings: %w", err)
		}
		raw, _ := d.GetRaw(key)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil
		}
		v, ok := fields[from]
		if !ok {
			return nil
		}
		delete(fields, from)
		fields[to] = v
		next, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if err := d.SetRaw(key, string(next)); err != nil {
			return err
		}
		return r.writeSettings(tx, d, stored)
	})
	if err == nil {
		r.s.settingsChanged()
	}
	return err
}

// SetPasswordHash stores a new dashboard password hash.
func (r *SettingsRepo) SetPasswordHash(ctx context.Context, hash string) error {
	return r.patch(ctx, map[string]string{"password": document.Encode(dialect.StripNUL(hash))})
}

func (r *SettingsRepo) patch(ctx context.Context, patch map[string]string) error {
	for _, k := range sortedKeys(patch) {
		if k == "" || !gjson.Valid(patch[k]) {
			return fmt.Errorf("repository: settings value for %q is not valid JSON", k)
		}
	}
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		d, stored, err := r.s.readStored(tx)
		if err != nil {
			return fmt.Errorf("settings: %w", err)
		}
		for _, k := range sortedKeys(patch) {
			if err := d.SetRaw(k, patch[k]); err != nil {
				return err
			}
		}
		return r.writeSettings(tx, d, stored)
	})
	if err == nil {
		r.s.settingsChanged()
	}
	return err
}
