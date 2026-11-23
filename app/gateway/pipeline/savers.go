package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"rllmx/app/gateway/saver"
	"rllmx/app/gateway/saver/headroom"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

// saverBypassHeader turns every token saver off for one request. It keeps the
// reference's name so an existing client keeps working.
const saverBypassHeader = "X-9router-Token-Saver"

// saverOptions reads the saver keys for this request. A request with the bypass
// header, a missing settings reader or a failed read runs no saver.
func (c *call) saverOptions(ctx context.Context) saver.Options {
	if c.p == nil || c.headerOff(saverBypassHeader) || c.p.d.Settings == nil {
		return saver.Options{}
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return saver.Options{}
	}
	o := readSaverOptions(doc, c.p.d.SaverURL)
	if !o.RTK && !o.Headroom.Enabled && !o.Caveman.Enabled && !o.Ponytail.Enabled && !o.Custom.Enabled {
		return saver.Options{}
	}
	return o
}

// readSaverOptions projects the stored row, taking the default for a missing or
// unusable value.
func readSaverOptions(doc document.Doc, configURL string) saver.Options {
	ms := min(max(doc.Int("headroomTimeoutMs", headroom.DefaultTimeoutMS), headroom.MinTimeoutMS), headroom.MaxTimeoutMS)
	return saver.Options{
		RTK: doc.Bool("rtkEnabled", true),
		Headroom: saver.Headroom{
			Enabled:              doc.Bool("headroomEnabled", false),
			URL:                  text.FirstNonEmpty(doc.Str("headroomUrl", ""), configURL, headroom.DefaultURL),
			CompressUserMessages: doc.Bool("headroomCompressUserMessages", false),
			Timeout:              time.Duration(ms) * time.Millisecond,
		},
		Caveman: saver.Prompt{
			Enabled: doc.Bool("cavemanEnabled", false), Level: doc.Str("cavemanLevel", "full"),
			Mode: rawMode(doc, "cavemanRoutingMode", "always"),
		},
		Ponytail: saver.Prompt{
			Enabled: doc.Bool("ponytailEnabled", false), Level: doc.Str("ponytailLevel", "full"),
			Mode: rawMode(doc, "ponytailRoutingMode", "smart"),
		},
		Custom: saver.Custom{Enabled: doc.Bool("customPromptEnabled", false), Text: doc.Str("customPrompt", "")},
	}
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
		promptKey("cav", o.Caveman),
		promptKey("pon", o.Ponytail),
		"cus=" + strconv.FormatBool(o.Custom.Enabled),
		"h=" + hex.EncodeToString(sum[:6]),
	}, ";")
}

// promptKey names a prompt saver in the response key: off, or its level and mode.
// A level kept for a switched-off saver changes nothing an answer carries.
func promptKey(name string, p saver.Prompt) string {
	if !p.Enabled {
		return name + "=off"
	}
	return name + "=" + p.Level + "/" + p.Mode
}

// applySavers runs the configured savers on the hub request before it is
// written, and logs one line of counts when something changed.
func (c *call) applySavers(ctx context.Context) {
	opts := c.saverOptions(ctx)
	if opts == (saver.Options{}) {
		return
	}
	st := saver.Apply(ctx, c.p.d.Saver, c.req, opts)
	c.saverStats = st
	if st.Changed() && c.p.d.Log != nil {
		c.p.d.Log.Debugf("saver: %s", st.Line())
	}
}

// rawMode reads an activation mode; anything but the two known values is the
// default, so a typo cannot silently switch a saver on.
func rawMode(doc document.Doc, key, def string) string {
	switch v := doc.Str(key, def); v {
	case "smart", "always":
		return v
	}
	return def
}
