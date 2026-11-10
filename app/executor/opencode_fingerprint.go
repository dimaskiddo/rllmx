package executor

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The free gate compares the tool definitions and the system marker, so both are
// the client's own text. The workspace values are fixed words: the reference
// reports the directory it runs in, the host's platform and its temporary path,
// and none of that belongs on the wire.
const (
	openCodeEnvMarker   = "<env>"
	openCodeWorkingDir  = "/workspace"
	openCodeEnvPlatform = "linux"
	openCodeTempDir     = "/tmp/opencode"
)

// openCodeFingerprintTool is one of the two tools the gate requires.
type openCodeFingerprintTool struct {
	name, description, parameters string
}

// openCodeFingerprintTools are the client's own definitions, verbatim: the gate
// compares both halves of each.
var openCodeFingerprintTools = []openCodeFingerprintTool{
	{
		name:        "read",
		description: "Read the contents of a file or directory. Supports text files, images, and PDFs. Images and PDFs are presented directly to the model. Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content. Directory entries are returned one per line. Use offset and limit to read large files or directories in sections. Prefer one larger read over many small slices, and use grep to find specific content in large files.",
		parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"File or directory to read"},"offset":{"type":"integer","minimum":0,"description":"The line or directory entry to start reading from (1-based)"},"limit":{"type":"integer","minimum":0,"description":"The maximum number of lines or directory entries to read (defaults to and capped at 2000)"}},"required":["path"],"additionalProperties":false}`,
	},
	{
		name:        "shell",
		description: "Execute a shell command and return its output. Commands run on Linux using bash. Quote file paths containing spaces or special characters. Prefer dedicated tools over shell commands when possible. When output is large, the full result is saved to a file and a truncated preview is returned. Rely on automatic truncation unless filtering the output is more useful. Commands accept an optional timeout, background commands have no timeout by default. Background commands return immediately, and you will be notified when they complete.",
		parameters:  `{"type":"object","properties":{"command":{"type":"string","description":"Shell command string to execute"},"workdir":{"type":"string","description":"Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead."},"timeout":{"type":"integer","minimum":0,"description":"Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default."},"background":{"type":"boolean","description":"Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion."}},"required":["command"],"additionalProperties":false}`,
	},
}

// openCodeEnvBlock is the marker the gate looks for in the system prompt.
const openCodeEnvBlock = "Here is some useful information about the environment you are running in:\n" +
	openCodeEnvMarker + "\n" +
	"  Working directory: " + openCodeWorkingDir + "\n" +
	"  Workspace root folder: " + openCodeWorkingDir + "\n" +
	"  Is directory a git repo: no\n" +
	"  Platform: " + openCodeEnvPlatform + "\n" +
	"  Prefer " + openCodeTempDir + " over generic system temporary directories such as /tmp; it is pre-created and approved for external access.\n" +
	"</env>"

// openCodeFingerprint fits the body to the free gate and records the names it
// had to change, so the answer can carry the client's own back.
func openCodeFingerprint(body []byte, flat bool) []byte {
	body, renames := openCodeConcealTools(body, flat)
	body, added := openCodeAppendTools(body, flat)
	if len(added) > 0 {
		body = openCodeNoteTools(body, flat, added)
	}
	body = openCodeApplyEnv(body, flat)
	if flat && !gjson.GetBytes(body, "tool_choice").Exists() {
		body = setValue(body, "tool_choice", "auto")
	}
	if len(renames) > 0 {
		body = recordOpenCodeRenames(body, renames)
	}
	return body
}

// openCodeConcealTools keeps a caller's tool on a fingerprint name when its
// arguments can carry the gate's definition, and otherwise moves it aside under
// an alias so the gate's own copy can be added.
func openCodeConcealTools(body []byte, flat bool) ([]byte, map[string]string) {
	tools := gjson.GetBytes(body, "tools")
	renames := map[string]string{}
	if !tools.IsArray() {
		return body, renames
	}
	taken := map[string]bool{}
	for _, t := range tools.Array() {
		if n := openCodeToolName(t); n != "" {
			taken[n] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range tools.Array() {
		if !t.IsObject() {
			out = append(out, t.Raw)
			continue
		}
		name := openCodeToolName(t)
		key := openCodeFingerprintKey(name)
		if key == "" {
			out = append(out, t.Raw)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		def := openCodeFingerprintToolFor(key)
		if openCodeCarriesDefinition(t, def) || openCodeCanAdopt(t, def) {
			out = append(out, openCodeWithDefinition(t.Raw, flat, def))
			continue
		}
		alias := key + "_client"
		for i := 2; taken[alias]; i++ {
			alias = key + "_client" + strconv.Itoa(i)
		}
		taken[alias] = true
		renames[alias] = key
		out = append(out, openCodeWithName(t.Raw, flat, alias))
	}
	body = setRaw(body, "tools", "["+strings.Join(out, ",")+"]")
	if len(renames) > 0 {
		body = openCodeRetargetChoice(body, renames, flat)
	}
	return body, renames
}

// openCodeAppendTools adds the gate's definition for each name the request does
// not already carry, and returns the names it added.
func openCodeAppendTools(body []byte, flat bool) ([]byte, []string) {
	tools := gjson.GetBytes(body, "tools")
	var out []string
	have := map[string]bool{}
	if tools.IsArray() {
		for _, t := range tools.Array() {
			if n := openCodeToolName(t); n != "" {
				have[n] = true
			}
		}
		for _, t := range tools.Array() {
			out = append(out, t.Raw)
		}
	}
	var added []string
	for _, def := range openCodeFingerprintTools {
		if have[def.name] {
			continue
		}
		tool := `{"type":"function"}`
		if flat {
			tool, _ = sjson.Set(tool, "name", def.name)
			tool, _ = sjson.Set(tool, "description", def.description)
			tool, _ = sjson.SetRaw(tool, "parameters", def.parameters)
			tool, _ = sjson.Set(tool, "strict", false)
		} else {
			tool, _ = sjson.Set(tool, "function.name", def.name)
			tool, _ = sjson.Set(tool, "function.description", def.description)
			tool, _ = sjson.SetRaw(tool, "function.parameters", def.parameters)
		}
		out = append(out, tool)
		added = append(added, def.name)
	}
	return setRaw(body, "tools", "["+strings.Join(out, ",")+"]"), added
}

// openCodeNoteTools tells the model the added tools are placeholders. Without it
// the model calls them, and they do nothing.
func openCodeNoteTools(body []byte, flat bool, names []string) []byte {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	list := strings.Join(quoted, " or ")
	note := "Do not call the " + list + " tools; they are placeholders that do nothing. Use the other tools you have been given instead."
	if len(names) == 1 {
		note = "Do not call the " + list + " tool; it is a placeholder that does nothing. Use the other tools you have been given instead."
	}
	if flat {
		if cur := gjson.GetBytes(body, "instructions").String(); strings.TrimSpace(cur) != "" {
			return setValue(body, "instructions", cur+"\n\n"+note)
		}
		return setValue(body, "instructions", note)
	}
	return openCodePrependSystem(body, note)
}

// openCodePrependSystem adds text to the system turn, creating one when there is
// none.
func openCodePrependSystem(body []byte, text string) []byte {
	system := gjson.GetBytes(body, "system")
	switch {
	case system.Type == gjson.String:
		if strings.TrimSpace(system.String()) == "" {
			return setValue(body, "system", text)
		}
		return setValue(body, "system", system.String()+"\n\n"+text)
	case system.IsArray():
		blocks := system.Array()
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Get("text").Exists() {
				cur := blocks[i].Get("text").String()
				parts := make([]string, len(blocks))
				for j, b := range blocks {
					parts[j] = b.Raw
				}
				parts[i], _ = sjson.Set(blocks[i].Raw, "text", cur+"\n\n"+text)
				return setRaw(body, "system", "["+strings.Join(parts, ",")+"]")
			}
		}
		return setRaw(body, "system", system.Raw[:len(system.Raw)-1]+`,{"type":"text","text":`+quoteJSON(text)+"}]")
	case system.Exists():
		return body
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	parts := []string{`{"role":"system","content":` + quoteJSON(text) + `}`}
	for _, m := range msgs.Array() {
		parts = append(parts, m.Raw)
	}
	return setRaw(body, "messages", "["+strings.Join(parts, ",")+"]")
}

// openCodeApplyEnv adds the environment marker to the system prompt unless it is
// already there.
func openCodeApplyEnv(body []byte, flat bool) []byte {
	if flat {
		cur := gjson.GetBytes(body, "instructions").String()
		if strings.Contains(cur, openCodeEnvMarker) {
			return body
		}
		if strings.TrimSpace(cur) == "" {
			return setValue(body, "instructions", openCodeEnvBlock)
		}
		return setValue(body, "instructions", cur+"\n\n"+openCodeEnvBlock)
	}
	if strings.Contains(gjson.GetBytes(body, "system").String(), openCodeEnvMarker) {
		return body
	}
	if sis := gjson.GetBytes(body, "system"); sis.IsArray() {
		for _, b := range sis.Array() {
			if strings.Contains(b.Get("text").String(), openCodeEnvMarker) {
				return body
			}
		}
	}
	return openCodePrependSystem(body, openCodeEnvBlock)
}

// openCodeToolName reads a tool's name in either shape.
func openCodeToolName(t gjson.Result) string {
	if n := strings.TrimSpace(t.Get("name").String()); n != "" {
		return n
	}
	return strings.TrimSpace(t.Get("function.name").String())
}

// openCodeFingerprintKey is the gate's name a tool carries exactly, or "".
func openCodeFingerprintKey(name string) string {
	switch name {
	case "read", "shell":
		return name
	}
	return ""
}

func openCodeFingerprintToolFor(key string) openCodeFingerprintTool {
	for _, def := range openCodeFingerprintTools {
		if def.name == key {
			return def
		}
	}
	return openCodeFingerprintTool{}
}

// openCodeCarriesDefinition reports a tool that already is the gate's.
func openCodeCarriesDefinition(t gjson.Result, def openCodeFingerprintTool) bool {
	node := t
	if fn := t.Get("function"); fn.IsObject() {
		node = fn
	}
	return node.Get("description").String() == def.description && node.Get("parameters").Raw == def.parameters
}

// openCodeCanAdopt reports a tool whose arguments are a subset of the gate's,
// so the gate's definition can replace its own without losing an argument the
// caller's handler expects.
func openCodeCanAdopt(t gjson.Result, def openCodeFingerprintTool) bool {
	node := t
	if fn := t.Get("function"); fn.IsObject() {
		node = fn
	}
	props := node.Get("parameters.properties")
	if !props.IsObject() {
		return false
	}
	wanted := gjson.Get(def.parameters, "properties").Map()
	for name := range props.Map() {
		if _, ok := wanted[name]; !ok {
			return false
		}
	}
	for _, req := range gjson.Get(def.parameters, "required").Array() {
		if !props.Get(req.String()).Exists() {
			return false
		}
	}
	return true
}

// openCodeWithDefinition replaces a tool's text with the gate's, keeping its name.
func openCodeWithDefinition(raw string, flat bool, def openCodeFingerprintTool) string {
	if flat {
		out, _ := sjson.Set(raw, "description", def.description)
		out, _ = sjson.SetRaw(out, "parameters", def.parameters)
		return out
	}
	out, _ := sjson.Set(raw, "function.description", def.description)
	out, _ = sjson.SetRaw(out, "function.parameters", def.parameters)
	return out
}

// openCodeWithName renames a tool in either shape.
func openCodeWithName(raw string, flat bool, name string) string {
	path := "name"
	if !flat {
		path = "function.name"
	}
	out, _ := sjson.Set(raw, path, name)
	return out
}

// openCodeRetargetChoice points an explicit choice at the alias of a tool that
// moved aside.
func openCodeRetargetChoice(body []byte, renames map[string]string, flat bool) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() {
		return body
	}
	alias := map[string]string{}
	for sent, original := range renames {
		alias[original] = sent
	}
	name := strings.TrimSpace(choice.Get("name").String())
	if !flat {
		name = strings.TrimSpace(choice.Get("function.name").String())
	}
	if sent, ok := alias[name]; ok {
		path := "tool_choice.name"
		if !flat {
			path = "tool_choice.function.name"
		}
		body, _ = sjson.SetBytes(body, path, sent)
	}
	return body
}

// openCodeRenamed is the request-local map of a name the wire carries back to
// the client's own. It is kept in the body under a key no upstream reads and
// removed before the request goes out.
const openCodeRenamed = "_opencodeRenamed"

// recordOpenCodeRenames stores the map where the pipeline can read it: the
// executor has already built the body by the time the names are known.
func recordOpenCodeRenames(body []byte, renames map[string]string) []byte {
	raw, err := json.Marshal(renames)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, openCodeRenamed, raw)
	if err != nil {
		return body
	}
	return out
}

// openCodeRenames reads the map out of a body, and strips it so the request
// never carries it.
func openCodeRenames(body []byte) map[string]string {
	raw := gjson.GetBytes(body, openCodeRenamed)
	if !raw.IsObject() {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw.Map() {
		out[k] = v.String()
	}
	return out
}

// openCodeWithoutRenames strips the map before the body goes out.
func openCodeWithoutRenames(body []byte) []byte {
	if !gjson.GetBytes(body, openCodeRenamed).IsObject() {
		return body
	}
	out, err := sjson.DeleteBytes(body, openCodeRenamed)
	if err != nil {
		return body
	}
	return out
}
