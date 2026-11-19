package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/saver"
	"rllmx/app/store/document"
)

// saverBypassHeader turns every token saver off for one request. It keeps the
// reference's name so an existing client keeps working.
const saverBypassHeader = "X-9router-Token-Saver"

// saverDefaultURL is the sidecar address when neither the settings row nor the
// configuration names one.
const saverDefaultURL = "http://localhost:8787"

// saverSettings is the secret-free projection of the saver keys.
type saverSettings struct {
	RTK         bool   `json:"rtk"`
	Sidecar     bool   `json:"sidecar"`
	URL         string `json:"url,omitempty"`
	SidecarUser bool   `json:"sidecarUser,omitempty"`
	SidecarMS   int    `json:"sidecarMs,omitempty"`
	Caveman     bool   `json:"caveman,omitempty"`
	CavemanLvl  string `json:"cavemanLvl,omitempty"`
	CavemanMode string `json:"cavemanMode,omitempty"`
	Ponytail    bool   `json:"ponytail,omitempty"`
	PonyLvl     string `json:"ponyLvl,omitempty"`
	PonyMode    string `json:"ponyMode,omitempty"`
	Custom      bool   `json:"custom,omitempty"`
	CustomText  string `json:"customText,omitempty"`
}

// saverOff is true when the client asked for the upstream body untouched.
func (c *call) saverOff() bool {
	return strings.EqualFold(strings.TrimSpace(c.in.Header.Get(saverBypassHeader)), "off")
}

// saverOptions reads the saver keys for this request. A request with the bypass
// header, a missing settings reader or a failed read runs no saver.
func (c *call) saverOptions(ctx context.Context) saver.Options {
	if c.p == nil || c.saverOff() || c.p.d.Settings == nil {
		return saver.Options{}
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return saver.Options{}
	}
	s := readSaverSettings(doc, c.p.d.SaverURL)
	if !s.RTK && !s.Sidecar && !s.Caveman && !s.Ponytail && !s.Custom {
		return saver.Options{}
	}
	return saver.Options{
		RTK: s.RTK,
		Headroom: saver.Headroom{
			Enabled: s.Sidecar, URL: s.URL, CompressUserMessages: s.SidecarUser,
			Timeout: time.Duration(s.SidecarMS) * time.Millisecond,
		},
		Caveman:  saver.Prompt{Enabled: s.Caveman, Level: s.CavemanLvl, Mode: s.CavemanMode},
		Ponytail: saver.Prompt{Enabled: s.Ponytail, Level: s.PonyLvl, Mode: s.PonyMode},
		Custom:   saver.Custom{Enabled: s.Custom, Text: s.CustomText},
	}
}

// readSaverSettings projects the stored row, taking overrides for a missing or
// unusable value.
func readSaverSettings(doc document.Doc, configURL string) saverSettings {
	s := saverSettings{
		RTK:         rawTrue(doc, "rtkEnabled", true),
		Sidecar:     rawTrue(doc, "headroomEnabled", false),
		URL:         firstNonEmpty(rawString(doc, "headroomUrl", ""), configURL, saverDefaultURL),
		SidecarUser: rawTrue(doc, "headroomCompressUserMessages", false),
		SidecarMS:   clampInt(rawInt(doc, "headroomTimeoutMs", 3000), 100, 60000),
		Caveman:     rawTrue(doc, "cavemanEnabled", false),
		CavemanLvl:  rawString(doc, "cavemanLevel", "full"),
		CavemanMode: rawMode(doc, "cavemanRoutingMode", "always"),
		Ponytail:    rawTrue(doc, "ponytailEnabled", false),
		PonyLvl:     rawString(doc, "ponytailLevel", "full"),
		PonyMode:    rawMode(doc, "ponytailRoutingMode", "smart"),
		Custom:      rawTrue(doc, "customPromptEnabled", false),
		CustomText:  rawString(doc, "customPrompt", ""),
	}
	return s
}

// saverKey is the part of the response-cache key that names what the savers
// would change: an answer produced with one prompt or level is never served for
// another. The custom text and the address enter as a hash.
func (c *call) saverKey(ctx context.Context) string {
	o := c.saverOptions(ctx)
	if !o.RTK && !o.Headroom.Enabled && !o.Caveman.Enabled && !o.Ponytail.Enabled && !o.Custom.Enabled {
		return ""
	}
	sum := sha256.Sum256([]byte(o.Headroom.URL + "\x00" + o.Custom.Text))
	return strings.Join([]string{
		"rtk=" + strconv.FormatBool(o.RTK),
		"ho=" + strconv.FormatBool(o.Headroom.Enabled),
		"hou=" + strconv.FormatBool(o.Headroom.CompressUserMessages),
		"cav=" + o.Caveman.Level + "/" + o.Caveman.Mode,
		"pon=" + o.Ponytail.Level + "/" + o.Ponytail.Mode,
		"cus=" + strconv.FormatBool(o.Custom.Enabled),
		"h=" + hex.EncodeToString(sum[:6]),
	}, ";")
}

// applySavers runs the configured savers on the hub request before it is
// written, and logs one line of counts when something changed.
func (c *call) applySavers(ctx context.Context) {
	opts := c.saverOptions(ctx)
	if opts == (saver.Options{}) {
		return
	}
	st := saver.Apply(ctx, c.p.d.Saver, c.req, opts)
	if st.Changed() && c.p.d.Log != nil {
		c.p.d.Log.Debugf("saver: %s", st.Line())
	}
}

func rawTrue(doc document.Doc, key string, def bool) bool {
	raw, ok := doc.GetRaw(key)
	if !ok {
		return def
	}
	v := gjson.Parse(raw)
	if v.Type != gjson.True && v.Type != gjson.False {
		return def
	}
	return v.Bool()
}

func rawString(doc document.Doc, key, def string) string {
	raw, ok := doc.GetRaw(key)
	if !ok {
		return def
	}
	if v := gjson.Parse(raw); v.Type == gjson.String {
		return v.String()
	}
	return def
}

func rawInt(doc document.Doc, key string, def int) int {
	raw, ok := doc.GetRaw(key)
	if !ok {
		return def
	}
	if v := gjson.Parse(raw); v.Type == gjson.Number {
		return int(v.Int())
	}
	return def
}

// rawMode reads an activation mode; anything but the two known values is the
// default, so a typo cannot silently switch a saver on.
func rawMode(doc document.Doc, key, def string) string {
	switch v := rawString(doc, key, def); v {
	case "smart", "always":
		return v
	}
	return def
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
