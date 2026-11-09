package translate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/document"
)

// Disguise makes a Claude request from another client look like Claude Code's
// own. It is the behavior of the tool the login belongs to, with no switch: a
// deployer who does not want it does not connect a Claude login.
type Disguise struct {
	// Seed names the account. The device and account ids derive from it, so they
	// stay the same for an account across token refreshes.
	Seed string
	// Session is the conversation id, without any "claude:" prefix.
	Session string
	// Tools renames the client's tools and adds the decoys.
	Tools bool
}

const (
	toolSuffix     = "_ide"
	billingPrefix  = "x-anthropic-billing-header:"
	billingVersion = "2.1.280"
	decoyNote      = "This tool is currently unavailable."
)

// Names of Claude Code's own tools. They are added unchanged as decoys, and a
// name in this list is never restored by dropping a suffix.
var decoyNames = []string{
	"Task", "TaskOutput", "TaskStop", "TaskCreate", "TaskGet", "TaskUpdate", "TaskList",
	"Bash", "Glob", "Grep", "Read", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch",
	"AskUserQuestion", "Skill", "EnterPlanMode", "ExitPlanMode",
}

var decoySet = func() map[string]bool {
	m := make(map[string]bool, len(decoyNames))
	for _, n := range decoyNames {
		m[n] = true
	}
	return m
}()

// Cloak applies the disguise to a finished Claude body. It returns the new body
// and the map from each renamed tool back to the client's name; the map is nil
// when no tool was renamed. A body that is not a JSON object comes back as it
// was.
func Cloak(body []byte, d *Disguise) ([]byte, map[string]string) {
	if d == nil || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body, nil
	}
	out := withBilling(body)
	out = withUserID(out, d)
	if !d.Tools {
		return out, nil
	}
	return cloakTools(out)
}

// withBilling puts the billing block first in the system prompt. The hash is
// taken over the body as it stands, before tools are renamed.
func withBilling(body []byte) []byte {
	sys := gjson.GetBytes(body, "system")
	if sys.IsArray() {
		if first := sys.Get("0.text").String(); strings.HasPrefix(first, billingPrefix) {
			return body
		}
	}
	block := `{"type":"text","text":` + quote(billingText(body)) + `}`
	var blocks string
	switch {
	case sys.IsArray():
		blocks = "[" + block
		for _, b := range sys.Array() {
			blocks += "," + b.Raw
		}
		blocks += "]"
	case sys.Type == gjson.String:
		blocks = "[" + block + `,{"type":"text","text":` + quote(sys.String()) + `}]`
	default:
		blocks = "[" + block + "]"
	}
	out, err := sjson.SetRawBytes(body, "system", []byte(blocks))
	if err != nil {
		return body
	}
	return out
}

func billingText(body []byte) string {
	sum := sha256.Sum256(body)
	build := make([]byte, 2)
	_, _ = rand.Read(build)
	return fmt.Sprintf("%s cc_version=%s.%s; cc_entrypoint=sdk-cli; cch=%s;",
		billingPrefix, billingVersion, hex.EncodeToString(build)[:3], hex.EncodeToString(sum[:])[:5])
}

// withUserID adds Claude Code's user id when the client sent none.
func withUserID(body []byte, d *Disguise) []byte {
	if gjson.GetBytes(body, "metadata.user_id").String() != "" {
		return body
	}
	out, err := sjson.SetBytes(body, "metadata.user_id", userID(d))
	if err != nil {
		return body
	}
	return out
}

// userID is {"device_id":…,"account_uuid":…,"session_id":…}, in that order.
func userID(d *Disguise) string {
	device := sha256.Sum256([]byte("device:" + d.Seed))
	session := cleanSession(d.Session)
	if session == "" || strings.ContainsAny(session, `"\`) || strings.ContainsFunc(session, func(r rune) bool { return r < 0x20 }) {
		session = randomUUID()
	}
	return `{"device_id":"` + hex.EncodeToString(device[:]) + `","account_uuid":"` + deriveUUID("account:"+d.Seed) + `","session_id":"` + session + `"}`
}

// SessionOf reads the session id back out of a user id, which is how the session
// header is made to agree with the body.
func SessionOf(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	if userID[0] == '{' {
		return cleanSession(gjson.Get(userID, "session_id").String())
	}
	return cleanSession(userID)
}

func cleanSession(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "claude:") {
		s = strings.TrimSpace(s[7:])
	}
	return s
}

// deriveUUID is a v4-shaped id derived from a seed.
func deriveUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:])
	variant := "89ab"[sum[8]&0x3]
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + string(variant) + h[17:20] + "-" + h[20:32]
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// cloakTools renames the client's tools, the tool calls in the history and a
// forced tool choice, then appends the decoys. A body with no tools is left
// alone: there is nothing to hide and no decoy is added.
func cloakTools(body []byte) ([]byte, map[string]string) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return body, nil
	}
	names := map[string]string{}
	renamed := map[string]bool{}
	var list []string
	for _, t := range tools.Array() {
		raw := t.Raw
		if t.Get("type").Exists() {
			list = append(list, raw)
			continue
		}
		name := t.Get("name").String()
		suffixed := name + toolSuffix
		names[suffixed] = name
		renamed[name] = true
		if out, err := sjson.Set(raw, "name", suffixed); err == nil {
			raw = out
		}
		list = append(list, raw)
	}
	for _, n := range decoyNames {
		list = append(list, `{"name":`+quote(n)+`,"description":`+quote(decoyNote)+`,"input_schema":{"type":"object","properties":{}}}`)
	}
	out, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(list, ",")+"]"))
	if err != nil {
		return body, nil
	}
	out = renameHistory(out)
	if tc := gjson.GetBytes(out, "tool_choice"); tc.Get("type").String() == "tool" && renamed[tc.Get("name").String()] {
		if next, err := sjson.SetBytes(out, "tool_choice.name", tc.Get("name").String()+toolSuffix); err == nil {
			out = next
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	return out, names
}

// renameHistory suffixes the name of every tool call in the conversation, which
// the upstream checks against the tool list.
func renameHistory(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	// Each message is edited on its own text and the array is written back once,
	// so the whole body is copied one time however many calls the history holds.
	var out strings.Builder
	changed := false
	last := 0 // offset in msgs.Raw already copied
	for _, m := range msgs.Array() {
		raw := m.Raw
		content := m.Get("content")
		if content.IsArray() {
			for j, b := range content.Array() {
				if b.Get("type").String() != "tool_use" {
					continue
				}
				next, err := sjson.Set(raw, "content."+strconv.Itoa(j)+".name", b.Get("name").String()+toolSuffix)
				if err != nil {
					continue
				}
				raw = next
			}
		}
		if raw == m.Raw {
			continue
		}
		if !changed {
			out.Grow(len(msgs.Raw) + 64)
			changed = true
		}
		start := m.Index - msgs.Index
		out.WriteString(msgs.Raw[last:start])
		out.WriteString(raw)
		last = start + len(m.Raw)
	}
	if !changed {
		return body
	}
	out.WriteString(msgs.Raw[last:])
	next, err := sjson.SetRawBytes(body, "messages", []byte(out.String()))
	if err != nil {
		return body
	}
	return next
}

// Uncloak gives a tool name back to the client. The map decides; a trailing
// suffix is dropped when the map misses it, so a name from an answer is
// restored even when the attempt was not cloaked and only the answer carries it
// (the reference's rule). A decoy the map lost is left alone, so the client
// sees "unavailable" rather than a silent call of a real tool.
func Uncloak(name string, names map[string]string, cloaked bool) string {
	if orig, ok := names[name]; ok {
		return orig
	}
	if decoySet[name] {
		return name
	}
	// The map is the only way a decoy name reaches the client renamed: a name
	// that strips back to a decoy is that decoy, not a client tool.
	base, ok := strings.CutSuffix(name, toolSuffix)
	if !ok || base == "" || decoySet[base] {
		return name
	}
	return base
}

func quote(s string) string { return document.Encode(s) }
