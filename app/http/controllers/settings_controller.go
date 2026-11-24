package controllers

import (
	"encoding/json"
	"math"
	nethttp "net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/tidwall/gjson"

	"rllmx/app/access/auth"
	"rllmx/app/access/oidc"
	"rllmx/app/gateway/account"
	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/plan"
	"rllmx/app/gateway/saver/headroom"
	"rllmx/app/gateway/skill"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// maxSettingsBody bounds one settings write; the whole row is smaller.
const maxSettingsBody = 256 << 10

// SettingsController answers the dashboard's settings routes. Only the keys the
// application reads are writable, and only those plus the computed facts are
// answered: a stored key nobody listed never leaves.
type SettingsController struct {
	DB   func() *repository.Store
	Auth *auth.Authenticator
	// Skills answers which config_schema key belongs to which installed field, so
	// a skill's own value is validated against its manifest. Nil refuses them.
	Skills *skill.Registry
	// Secure reports whether this request arrived over TLS, so the locale
	// cookie carries Secure where it can.
	Secure func(*nethttp.Request) bool
}

// settingsView is the allow list the dashboard reads.
type settingsView struct {
	RequireLogin  bool   `json:"requireLogin"`
	RequireAPIKey bool   `json:"requireApiKey"`
	AuthMode      string `json:"authMode"`
	SSOType       string `json:"ssoType"`

	OIDCIssuerURL  string `json:"oidcIssuerUrl"`
	OIDCClientID   string `json:"oidcClientId"`
	OIDCScopes     string `json:"oidcScopes"`
	OIDCLoginLabel string `json:"oidcLoginLabel"`
	OIDCConfigured bool   `json:"oidcConfigured"`

	SAMLEntryPoint     string `json:"samlEntryPoint"`
	SAMLIssuer         string `json:"samlIssuer"`
	SAMLCert           string `json:"samlCert"`
	SAMLIdPEntityID    string `json:"samlIdpEntityId"`
	SAMLLoginLabel     string `json:"samlLoginLabel"`
	SAMLAttributeEmail string `json:"samlAttributeEmail"`
	SAMLAttributeName  string `json:"samlAttributeName"`

	ComboStrategy              string          `json:"comboStrategy"`
	FallbackStrategy           string          `json:"fallbackStrategy"`
	ComboStrategies            json.RawMessage `json:"comboStrategies"`
	ComboStickyRoundRobinLimit int             `json:"comboStickyRoundRobinLimit"`
	StickyRoundRobinLimit      int             `json:"stickyRoundRobinLimit"`
	ProviderStrategies         json.RawMessage `json:"providerStrategies"`
	CapacityAdapter            json.RawMessage `json:"capacityAdapter"`
	ResponseCache              json.RawMessage `json:"responseCache"`

	// ClaudeAutoPing and CodexAutoPing are the reference's own values: false, or
	// an object holding the connections opted in and their schedules.
	ClaudeAutoPing json.RawMessage `json:"claudeAutoPing"`
	CodexAutoPing  json.RawMessage `json:"codexAutoPing"`

	RTKEnabled                  bool   `json:"rtkEnabled"`
	HeadroomEnabled             bool   `json:"headroomEnabled"`
	HeadroomURL                 string `json:"headroomUrl"`
	HeadroomCompressUserMessage bool   `json:"headroomCompressUserMessages"`
	HeadroomTimeoutMS           int    `json:"headroomTimeoutMs"`
	CavemanEnabled              bool   `json:"cavemanEnabled"`
	CavemanLevel                string `json:"cavemanLevel"`
	CavemanRoutingMode          string `json:"cavemanRoutingMode"`
	PonytailEnabled             bool   `json:"ponytailEnabled"`
	PonytailLevel               string `json:"ponytailLevel"`
	PonytailRoutingMode         string `json:"ponytailRoutingMode"`
	CustomPromptEnabled         bool   `json:"customPromptEnabled"`
	CustomPrompt                string `json:"customPrompt"`

	AutoPlanEnabled        bool   `json:"autoPlanEnabled"`
	AutoPlanMode           string `json:"autoPlanMode"`
	AutoPlanComboID        string `json:"autoPlanComboId"`
	AutoCodeComboID        string `json:"autoCodeComboId"`
	AutoPlanThreshold      int    `json:"autoPlanComplexityThreshold"`
	AutoPlanSmartClassify  bool   `json:"autoPlanSmartClassify"`
	AutoPlanMaxTokens      int    `json:"autoPlanMaxTokens"`
	AutoPlanTimeoutMS      int    `json:"autoPlanTimeoutMs"`
	AutoPlanShowInResponse bool   `json:"autoPlanShowInResponse"`

	SkillSources                 json.RawMessage `json:"skillSources"`
	ECCAutoSkillRouterEnabled    bool            `json:"ecc_auto_skill_routerEnabled"`
	ECCAutoSkillRouterConfidence float64         `json:"ecc_auto_skill_routerConfidence"`
	ECCAutoSkillRouterMaxSkills  int             `json:"ecc_auto_skill_routerMaxSkills"`

	HasPassword bool `json:"hasPassword"`
}

// Show answers GET /api/settings.
func (c *SettingsController) Show(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	d, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, c.view(d))
}

func (c *SettingsController) store() *repository.Store { return storeOf(c.DB) }

// Update answers PATCH /api/settings. Every key is validated before any is
// written, and a key this build does not read is refused rather than stored.
func (c *SettingsController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	r := ctx.Request().Origin()
	r.Body = nethttp.MaxBytesReader(ctx.Response().Writer(), r.Body, maxSettingsBody)
	var body map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil || dec.More() {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if len(body) == 0 {
		return flat(ctx, nethttp.StatusBadRequest, "No settings to change")
	}
	if len(body) > 32 {
		return flat(ctx, nethttp.StatusBadRequest, "Too many settings in one request")
	}
	patch := make(map[string]string, len(body))
	for key, raw := range body {
		switch key {
		case "newPassword", "currentPassword":
			return flat(ctx, nethttp.StatusBadRequest, "Change the password with POST /api/auth/change-password")
		}
		rule, ok := c.ruleFor(key)
		if !ok {
			return flat(ctx, nethttp.StatusBadRequest, "Unknown setting: "+text.CutRunes(text.StripControl(key), 64))
		}
		if string(raw) == "null" {
			return flat(ctx, nethttp.StatusBadRequest, "Setting "+key+" cannot be null")
		}
		val, msg := rule(raw)
		if msg != "" {
			return flat(ctx, nethttp.StatusBadRequest, "Setting "+key+": "+msg)
		}
		if key == "oidcClientSecret" && val == `""` {
			continue
		}
		patch[key] = val
	}
	if err := st.Settings().Patch(r.Context(), patch); err != nil {
		return storageDown(ctx)
	}
	d, err := st.Settings().Effective(r.Context())
	if err != nil {
		return storageDown(ctx)
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, c.view(d))
}

// view projects the stored settings through the allow list.
func (c *SettingsController) view(d document.Doc) settingsView {
	auto := plan.Read(d)
	v := settingsView{
		RequireLogin:                 d.Bool("requireLogin", true),
		RequireAPIKey:                d.Bool("requireApiKey", true),
		AuthMode:                     d.Str("authMode", "password"),
		SSOType:                      d.Str("ssoType", "oidc"),
		OIDCIssuerURL:                d.Str("oidcIssuerUrl", ""),
		OIDCClientID:                 d.Str("oidcClientId", ""),
		OIDCScopes:                   d.Str("oidcScopes", "openid profile email"),
		OIDCLoginLabel:               d.Str("oidcLoginLabel", "Sign in with OIDC"),
		SAMLEntryPoint:               d.Str("samlEntryPoint", ""),
		SAMLIssuer:                   d.Str("samlIssuer", ""),
		SAMLCert:                     d.Str("samlCert", ""),
		SAMLIdPEntityID:              d.Str("samlIdpEntityId", ""),
		SAMLLoginLabel:               d.Str("samlLoginLabel", "Sign in with SAML SSO"),
		SAMLAttributeEmail:           d.Str("samlAttributeEmail", "email"),
		SAMLAttributeName:            d.Str("samlAttributeName", "name"),
		ComboStrategy:                d.Str("comboStrategy", combo.DefaultStrategy),
		FallbackStrategy:             account.Strategy(d.Str("fallbackStrategy", "fill-first")),
		ComboStrategies:              rawObject(d, "comboStrategies", "{}"),
		ComboStickyRoundRobinLimit:   d.Int("comboStickyRoundRobinLimit", 1),
		StickyRoundRobinLimit:        d.Int("stickyRoundRobinLimit", 3),
		ProviderStrategies:           rawObject(d, "providerStrategies", "{}"),
		CapacityAdapter:              rawObject(d, "capacityAdapter", "{}"),
		ResponseCache:                rawObject(d, "responseCache", "{}"),
		ClaudeAutoPing:               rawPing(d, "claudeAutoPing"),
		CodexAutoPing:                rawPing(d, "codexAutoPing"),
		RTKEnabled:                   d.Bool("rtkEnabled", true),
		HeadroomEnabled:              d.Bool("headroomEnabled", false),
		HeadroomURL:                  d.Str("headroomUrl", ""),
		HeadroomCompressUserMessage:  d.Bool("headroomCompressUserMessages", false),
		HeadroomTimeoutMS:            d.Int("headroomTimeoutMs", headroom.DefaultTimeoutMS),
		CavemanEnabled:               d.Bool("cavemanEnabled", false),
		CavemanLevel:                 d.Str("cavemanLevel", "full"),
		CavemanRoutingMode:           d.Str("cavemanRoutingMode", "always"),
		PonytailEnabled:              d.Bool("ponytailEnabled", false),
		PonytailLevel:                d.Str("ponytailLevel", "full"),
		PonytailRoutingMode:          d.Str("ponytailRoutingMode", "smart"),
		CustomPromptEnabled:          d.Bool("customPromptEnabled", false),
		CustomPrompt:                 d.Str("customPrompt", ""),
		AutoPlanEnabled:              auto.Enabled,
		AutoPlanMode:                 auto.Mode,
		AutoPlanComboID:              auto.PlanCombo,
		AutoCodeComboID:              auto.CodeCombo,
		AutoPlanThreshold:            auto.Threshold,
		AutoPlanSmartClassify:        auto.SmartClassify,
		AutoPlanMaxTokens:            auto.MaxTokens,
		AutoPlanTimeoutMS:            auto.TimeoutMS,
		AutoPlanShowInResponse:       d.Bool("autoPlanShowInResponse", false),
		SkillSources:                 rawArray(d, "skillSources", "[]"),
		ECCAutoSkillRouterEnabled:    d.Bool("ecc_auto_skill_routerEnabled", false),
		ECCAutoSkillRouterConfidence: d.Float("ecc_auto_skill_routerConfidence", skill.DefaultThreshold),
		ECCAutoSkillRouterMaxSkills:  d.Int("ecc_auto_skill_routerMaxSkills", 1),
	}
	v.HasPassword = v.HasPassword || d.Str("password", "") != ""
	if c.Auth != nil && c.Auth.OIDCUsable != nil {
		v.OIDCConfigured = c.Auth.OIDCUsable(v.OIDCIssuerURL, v.OIDCClientID, d.Str("oidcClientSecret", ""))
	}
	return v
}

func rawOf(d document.Doc, key string) gjson.Result {
	raw, ok := d.GetRaw(key)
	if !ok {
		return gjson.Result{}
	}
	return gjson.Parse(raw)
}

func rawObject(d document.Doc, key, def string) json.RawMessage {
	if v := rawOf(d, key); v.IsObject() {
		return json.RawMessage(v.Raw)
	}
	return json.RawMessage(def)
}

func rawArray(d document.Doc, key, def string) json.RawMessage {
	if v := rawOf(d, key); v.IsArray() {
		return json.RawMessage(v.Raw)
	}
	return json.RawMessage(def)
}

// settingRule validates one writable key and returns the JSON to store, or a
// message the form shows. The map is the allow list.
type settingRule func(json.RawMessage) (string, string)

var settingsRules = map[string]settingRule{
	"requireLogin":                    boolRule,
	"requireApiKey":                   boolRule,
	"claudeAutoPing":                  autoPingRule,
	"codexAutoPing":                   autoPingRule,
	"authMode":                        enumRule(auth.ModeNames()...),
	"ssoType":                         enumRule("oidc", "saml"),
	"oidcIssuerUrl":                   issuerRule,
	"oidcClientId":                    textRule(512),
	"oidcClientSecret":                textRule(512),
	"oidcScopes":                      textRule(512),
	"oidcLoginLabel":                  textRule(100),
	"samlEntryPoint":                  urlRule("http", 2048),
	"samlIssuer":                      textRule(512),
	"samlCert":                        certRule,
	"samlIdpEntityId":                 textRule(512),
	"samlLoginLabel":                  textRule(100),
	"samlAttributeEmail":              textRule(128),
	"samlAttributeName":               textRule(128),
	"comboStrategy":                   enumRule(combo.StrategyFallback, combo.StrategyRoundRobin, combo.StrategySmart, combo.StrategyFusion),
	"fallbackStrategy":                strategyRule,
	"comboStickyRoundRobinLimit":      intRule(1, 1000),
	"stickyRoundRobinLimit":           intRule(1, 100),
	"comboStrategies":                 comboStrategiesRule,
	"providerStrategies":              providerStrategiesRule,
	"capacityAdapter":                 capacityAdapterRule,
	"responseCache":                   responseCacheRule,
	"rtkEnabled":                      boolRule,
	"headroomEnabled":                 boolRule,
	"headroomUrl":                     urlRule("http", 2048),
	"headroomCompressUserMessages":    boolRule,
	"headroomTimeoutMs":               intRule(headroom.MinTimeoutMS, headroom.MaxTimeoutMS),
	"cavemanEnabled":                  boolRule,
	"cavemanLevel":                    enumRule("lite", "full", "ultra", "wenyan-lite", "wenyan", "wenyan-ultra"),
	"cavemanRoutingMode":              enumRule("smart", "always"),
	"ponytailEnabled":                 boolRule,
	"ponytailLevel":                   enumRule("lite", "full", "ultra"),
	"ponytailRoutingMode":             enumRule("smart", "always"),
	"customPromptEnabled":             boolRule,
	"customPrompt":                    multilineRule(4000),
	"autoPlanEnabled":                 boolRule,
	"autoPlanMode":                    enumRule("auto", "manual"),
	"autoPlanComboId":                 textRule(128),
	"autoCodeComboId":                 textRule(128),
	"autoPlanComplexityThreshold":     intRule(plan.MinThreshold, plan.MaxThreshold),
	"autoPlanSmartClassify":           boolRule,
	"autoPlanMaxTokens":               intRule(plan.MinMaxTokens, plan.MaxMaxTokens),
	"autoPlanTimeoutMs":               intRule(plan.MinTimeoutMS, plan.MaxTimeoutMS),
	"autoPlanShowInResponse":          boolRule,
	"skillSources":                    urlListRule("https", 10, 2048),
	"ecc_auto_skill_routerEnabled":    boolRule,
	"ecc_auto_skill_routerConfidence": floatRule(0, 1),
	"ecc_auto_skill_routerMaxSkills":  intRule(1, 5),
}

// reSkillKey matches the per-skill keys the dashboard writes for an installed
// skill: <id>Enabled and <id>RoutingMode, with the id bounded to the manifest's
// alphabet. Without this an imported skill's own switch could not be saved.
var reSkillKey = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]{0,63})(Enabled|RoutingMode)$`)

// ruleFor answers the rule for a key: the fixed table, then the per-skill shape,
// then a config_schema field of an installed skill.
func (c *SettingsController) ruleFor(key string) (settingRule, bool) {
	if r, ok := settingsRules[key]; ok {
		return r, true
	}
	// Only a skill this build holds may have a switch, so the settings row cannot
	// be grown with a key nothing reads.
	if m := reSkillKey.FindStringSubmatch(key); m != nil && c.Skills != nil {
		if _, installed := c.Skills.Find(m[1]); installed {
			if m[2] == "Enabled" {
				return boolRule, true
			}
			return enumRule("smart", "always"), true
		}
	}
	if c.Skills != nil {
		if f, ok := c.Skills.FieldFor(key); ok {
			return skillFieldRule(f), true
		}
		for _, m := range c.Skills.List() {
			if m.LegacyEnabledKey == key {
				return boolRule, true
			}
		}
	}
	return nil, false
}

// skillFieldRule validates a value against the field's own type and stores it in
// the shape that type reads back.
func skillFieldRule(f skill.Field) settingRule {
	return func(raw json.RawMessage) (string, string) {
		v, msg := skill.CheckValue(f, raw)
		if msg != "" {
			return "", msg
		}
		switch f.Type {
		case "text", "enum":
			out, err := json.Marshal(v)
			if err != nil {
				return "", "must be a string"
			}
			return string(out), ""
		}
		return v, ""
	}
}

func boolRule(raw json.RawMessage) (string, string) {
	switch string(raw) {
	case "true", "false":
		return string(raw), ""
	}
	return "", "must be true or false"
}

// rawPing reads an auto-ping setting, which is false or an object.
func rawPing(d document.Doc, key string) json.RawMessage {
	switch v := rawOf(d, key); {
	case v.IsObject():
		return json.RawMessage(v.Raw)
	case v.Type == gjson.True:
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// autoPingRule accepts the reference's two shapes for an auto-ping setting: a
// switch, or an object naming the connections and their schedules. Unknown keys
// inside the object are kept, so a value another build wrote survives a save.
func autoPingRule(raw json.RawMessage) (string, string) {
	switch string(raw) {
	case "true", "false":
		return string(raw), ""
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "must be true, false, or an object of connections and schedules"
	}
	for key, value := range body {
		switch key {
		case "connections":
			var on map[string]bool
			if err := json.Unmarshal(value, &on); err != nil {
				return "", "connections must map a connection id to true or false"
			}
		case "schedules":
			var schedules map[string]json.RawMessage
			if err := json.Unmarshal(value, &schedules); err != nil {
				return "", "schedules must map a connection id to a schedule"
			}
			for _, s := range schedules {
				if msg := scheduleRule(s); msg != "" {
					return "", msg
				}
			}
		}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return "", "must be true, false, or an object of connections and schedules"
	}
	return string(out), ""
}

// scheduleRule checks one anchored ping schedule, the shape the scheduler reads.
func scheduleRule(raw json.RawMessage) string {
	var s struct {
		Enabled     *bool    `json:"enabled"`
		Anchor      string   `json:"anchor"`
		WindowHours *float64 `json:"windowHours"`
		SlotsPerDay *int     `json:"slotsPerDay"`
	}
	if err := json.Unmarshal(raw, &s); err != nil && string(raw) != "null" {
		return "a schedule must be an object"
	}
	if s.Anchor != "" {
		head, tail, ok := strings.Cut(s.Anchor, ":")
		hour, err1 := strconv.Atoi(head)
		minute, err2 := strconv.Atoi(tail)
		if !ok || err1 != nil || err2 != nil || hour > 23 || minute > 59 || hour < 0 || minute < 0 {
			return "an anchor must read HH:MM"
		}
	}
	window := 5.0
	if s.WindowHours != nil {
		window = *s.WindowHours
	}
	if window < 0.5 || window > 24 {
		return "windowHours must be between 0.5 and 24"
	}
	if s.SlotsPerDay != nil {
		max := int(24 / window)
		if max < 1 {
			max = 1
		}
		if *s.SlotsPerDay < 1 || *s.SlotsPerDay > max {
			return "slotsPerDay is out of range for the window"
		}
	}
	return ""
}

func floatRule(min, max float64) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return "", "must be a number"
		}
		if f < min || f > max {
			return "", "is out of range"
		}
		out, _ := json.Marshal(f)
		return string(out), ""
	}
}

// urlListRule bounds a JSON list of operator-set origins, the allowlist a skill
// sync may clone from. A bad entry is refused rather than dropped.
func urlListRule(scheme string, max, maxLen int) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return "", "must be a list of URLs"
		}
		if len(list) > max {
			return "", "holds too many entries"
		}
		for _, s := range list {
			u, err := url.Parse(s)
			if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || len(s) > maxLen {
				return "", "each entry must be a plain URL"
			}
			if scheme == "https" && u.Scheme != "https" {
				return "", "each entry must use https"
			}
		}
		out, _ := json.Marshal(list)
		return string(out), ""
	}
}

func intRule(min, max int) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", "must be a whole number"
		}
		if n < min || n > max {
			return "", "is out of range"
		}
		out, _ := json.Marshal(n)
		return string(out), ""
	}
}

// strategyRule accepts the two account strategies the selector acts on. A combo
// word stored here would save and behave as fill-first.
func strategyRule(raw json.RawMessage) (string, string) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", "must be a string"
	}
	if !account.KnownStrategy(s) {
		return "", "must be fill-first or round-robin"
	}
	out, _ := json.Marshal(s)
	return string(out), ""
}

func enumRule(values ...string) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "must be a string"
		}
		if !slices.Contains(values, s) {
			return "", "must be one of " + strings.Join(values, ", ")
		}
		out, _ := json.Marshal(s)
		return string(out), ""
	}
}

// textRule bounds a string and refuses a control character: a label or an id
// with one is a mistake, not a value.
func textRule(max int) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "must be a string"
		}
		if len(s) > max {
			return "", "is too long"
		}
		if text.HasControl(s) {
			return "", "has a control character"
		}
		out, _ := json.Marshal(s)
		return string(out), ""
	}
}

// multilineRule is textRule for a block of prose: it keeps line breaks and tabs
// and refuses every other control character.
func multilineRule(max int) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "must be a string"
		}
		if len(s) > max {
			return "", "is too long"
		}
		if !onlyNewlines(s) || strings.ContainsFunc(s, func(r rune) bool { return r >= 0x7f && r < 0xa0 }) {
			return "", "has a control character"
		}
		out, _ := json.Marshal(s)
		return string(out), ""
	}
}

// urlRule takes an absolute address with a host and no credentials. The scheme
// is the strictest the caller allows: https, or either for a local identity
// provider.
// issuerRule stores the issuer the way discovery reads it. The CLI canonicalizes
// through the same call, so the two writers can never disagree about a value that
// decides whether the password form is hidden.
func issuerRule(raw json.RawMessage) (string, string) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", "must be a string"
	}
	if s == "" {
		return `""`, ""
	}
	if len(s) > 2048 || text.HasControl(s) {
		return "", "is not a valid address"
	}
	issuer, err := oidc.ParseIssuer(s)
	if err != nil {
		return "", "must be an https address with no path, query or credentials; http only for this machine"
	}
	out, _ := json.Marshal(issuer)
	return string(out), ""
}

func urlRule(scheme string, max int) settingRule {
	return func(raw json.RawMessage) (string, string) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "must be a string"
		}
		if s == "" {
			out, _ := json.Marshal("")
			return string(out), ""
		}
		if len(s) > max || text.HasControl(s) {
			return "", "is not a valid address"
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
			return "", "is not a valid address"
		}
		if scheme == "https" && u.Scheme != "https" {
			return "", "must use https"
		}
		if scheme != "https" && u.Scheme != "https" && u.Scheme != "http" {
			return "", "must use http or https"
		}
		out, _ := json.Marshal(s)
		return string(out), ""
	}
}

func certRule(raw json.RawMessage) (string, string) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", "must be a string"
	}
	if len(s) > 64<<10 {
		return "", "is too long"
	}
	if text.HasControl(s) && !onlyNewlines(s) {
		return "", "is not a certificate"
	}
	out, _ := json.Marshal(s)
	return string(out), ""
}

// onlyNewlines allows the line breaks a PEM block carries and nothing else.
func onlyNewlines(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func validNameKey(k string) bool {
	return k != "" && len(k) <= 100 && !text.HasControl(k) && !strings.Contains(k, "/")
}

func comboStrategiesRule(raw json.RawMessage) (string, string) {
	if len(raw) > 64<<10 || !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return "", "must be an object"
	}
	m := gjson.ParseBytes(raw)
	if n := len(m.Map()); n > 100 {
		return "", "names too many combos"
	}
	validStrategy := func(v gjson.Result) bool {
		switch v.String() {
		case combo.StrategyFallback, combo.StrategyRoundRobin, combo.StrategySmart, combo.StrategyFusion:
			return true
		}
		return false
	}
	for name, e := range m.Map() {
		if !validNameKey(name) {
			return "", "has an invalid combo name"
		}
		if !e.IsObject() {
			return "", "has an entry that is not an object"
		}
		for k := range e.Map() {
			switch k {
			case "fallbackStrategy", "smartPolicy", "sessionSticky", "confidenceThreshold",
				"routerModel", "judgeModel", "fusionTuning":
			default:
				return "", "has an unknown combo setting"
			}
		}
		if s := e.Get("fallbackStrategy"); s.Exists() && !validStrategy(s) {
			return "", "has an unknown strategy"
		}
		if v := e.Get("confidenceThreshold"); v.Exists() && (v.Type != gjson.Number || v.Float() <= 0 || v.Float() > 1) {
			return "", "has a threshold outside 0 to 1"
		}
		for _, k := range []string{"routerModel", "judgeModel", "smartPolicy"} {
			if v := e.Get(k); v.Exists() && (v.Type != gjson.String || len(v.String()) > 256 || text.HasControl(v.String())) {
				return "", "has an invalid " + k
			}
		}
		if v := e.Get("sessionSticky"); v.Exists() && v.Type != gjson.True && v.Type != gjson.False {
			return "", "has a sessionSticky that is not true or false"
		}
		if t := e.Get("fusionTuning"); t.Exists() && !t.IsObject() {
			return "", "has a fusionTuning that is not an object"
		}
		for k, min := range map[string]int{"minPanel": 1, "stragglerGraceMs": 100, "panelHardTimeoutMs": 100} {
			if v := e.Get("fusionTuning." + k); v.Exists() {
				if v.Type != gjson.Number || v.Int() < int64(min) || v.Int() > 600000 {
					return "", "has a fusion tuning value out of range"
				}
			}
		}
	}
	return string(raw), ""
}

// providerStrategiesRule accepts each provider's strategy either as the bare
// word older rows hold or as the object the selector and a 9router database
// write. Keys the selector does not read are kept: they came with an import.
func providerStrategiesRule(raw json.RawMessage) (string, string) {
	if len(raw) > 32<<10 || !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return "", "must be an object"
	}
	for name, v := range gjson.ParseBytes(raw).Map() {
		if !validNameKey(name) {
			return "", "has an invalid provider name"
		}
		if v.Type == gjson.String && account.KnownStrategy(v.String()) {
			continue
		}
		if !v.IsObject() || !knownStrategyObject(v) {
			return "", "has an unknown strategy"
		}
	}
	return string(raw), ""
}

// knownStrategyObject checks the two keys the selector reads; any other key in
// the entry is left as it is.
func knownStrategyObject(v gjson.Result) bool {
	ok := true
	v.ForEach(func(k, e gjson.Result) bool {
		switch k.String() {
		case "fallbackStrategy":
			ok = e.Type == gjson.String && account.KnownStrategy(e.String())
		case "stickyRoundRobinLimit":
			ok = e.Type == gjson.Number && e.Num == math.Trunc(e.Num) && e.Int() >= 1 && e.Int() <= 100
		}
		return ok
	})
	return ok
}

func capacityAdapterRule(raw json.RawMessage) (string, string) {
	if len(raw) > 16<<10 || !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return "", "must be an object"
	}
	for kind, e := range gjson.ParseBytes(raw).Map() {
		switch kind {
		case "vision", "pdf", "audioInput", "videoInput":
		default:
			return "", "has an unknown kind"
		}
		if !e.IsObject() {
			return "", "has a kind that is not an object"
		}
		for _, k := range []string{"enabled", "roundRobin"} {
			if v := e.Get(k); v.Exists() && v.Type != gjson.True && v.Type != gjson.False {
				return "", "has a " + k + " that is not true or false"
			}
		}
		models := e.Get("models")
		if !models.Exists() {
			continue
		}
		if !models.IsArray() || len(models.Array()) > 50 {
			return "", "has a models list that is not 0 to 50 entries"
		}
		for _, id := range models.Array() {
			if id.Type != gjson.String || id.String() == "" || len(id.String()) > 256 || text.HasControl(id.String()) {
				return "", "has an invalid model id"
			}
		}
	}
	return string(raw), ""
}

func responseCacheRule(raw json.RawMessage) (string, string) {
	if len(raw) > 1024 || !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return "", "must be an object"
	}
	v := gjson.ParseBytes(raw)
	for k := range v.Map() {
		switch k {
		case "enabled", "ttlSeconds", "maxEntryKb":
		default:
			return "", "has an unknown field"
		}
	}
	if e := v.Get("enabled"); e.Exists() && e.Type != gjson.True && e.Type != gjson.False {
		return "", "has an enabled that is not true or false"
	}
	if t := v.Get("ttlSeconds"); t.Exists() && (t.Type != gjson.Number || t.Int() < 1 || t.Int() > 604800) {
		return "", "has a ttlSeconds outside 1 to 604800"
	}
	if k := v.Get("maxEntryKb"); k.Exists() && (k.Type != gjson.Number || k.Int() < 1 || k.Int() > 4096) {
		return "", "has a maxEntryKb outside 1 to 4096"
	}
	return string(raw), ""
}

// Locale answers POST /api/locale, the dashboard's language choice. It is
// public: a visitor picks the language of the sign-in page before signing in.
func (c *SettingsController) Locale(ctx contractshttp.Context) contractshttp.Response {
	var b struct {
		Locale string `json:"locale"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	switch b.Locale {
	case "en", "id":
	default:
		return flat(ctx, nethttp.StatusBadRequest, "Unsupported locale")
	}
	ctx.Response().Cookie(contractshttp.Cookie{
		Name: "locale", Value: b.Locale, Path: "/", MaxAge: 365 * 24 * 60 * 60,
		HttpOnly: false, SameSite: "lax", Secure: c.Secure != nil && c.Secure(ctx.Request().Origin()),
	})
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true, "locale": b.Locale})
}
