package skill

import (
	"encoding/json"
	"strings"
	"testing"
)

const configManifest = `{"id":"terse","name":"Terse","description":"short","hook":"system-prompt",` +
	`"config_schema":[` +
	`{"key":"level","type":"enum","label":"Level","default":"full","legacy_key":"terseLevel",` +
	`"options":[{"value":"lite","label":"Lite"},{"value":"full","label":"Full"}]},` +
	`{"key":"scale","type":"slider","label":"Scale","default":3,"min":1,"max":10},` +
	`{"key":"routing_mode","type":"enum","default":"always","options":[{"value":"smart"},{"value":"always"}]}` +
	`]}`

func TestTheReferenceConfigShapeLoads(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"manifest.json": configManifest, "prompt.txt": "{level} answers"})
	m, prompt, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("an array config_schema was refused: %v", err)
	}
	if m.Mode() != "always" {
		t.Fatalf("mode %q", m.Mode())
	}
	if len(m.Config) != 3 || m.Config[0].LegacyKey != "terseLevel" {
		t.Fatalf("fields %+v", m.Config)
	}
	if SettingKey(m, m.Config[0]) != "terseLevel" || SettingKey(m, m.Config[1]) != "terse_scale" {
		t.Fatalf("keys %q %q", SettingKey(m, m.Config[0]), SettingKey(m, m.Config[1]))
	}
	if prompt != "{level} answers" {
		t.Fatalf("prompt %q", prompt)
	}
}

func TestCheckValueRefusesBadValues(t *testing.T) {
	level := Field{Key: "level", Type: "enum", Options: []Option{{Value: "lite"}, {Value: "full"}}}
	min, max := 1.0, 10.0
	scale := Field{Key: "scale", Type: "slider", Min: &min, Max: &max}
	note := Field{Key: "note", Type: "text"}
	on := Field{Key: "on", Type: "toggle"}
	for name, c := range map[string]struct {
		f    Field
		raw  string
		want string
		bad  string
	}{
		"enum ok":        {level, `"lite"`, "lite", ""},
		"enum unknown":   {level, `"ultra"`, "", "options"},
		"enum not text":  {level, `3`, "", "string"},
		"slider ok":      {scale, `4`, "4", ""},
		"slider under":   {scale, `0`, "", "at least"},
		"slider over":    {scale, `11`, "", "at most"},
		"slider not num": {scale, `"x"`, "", "number"},
		"text ok":        {note, `"hello"`, "hello", ""},
		"text control":   {note, `"a\u0007b"`, "", "control"},
		"text long":      {note, `"` + strings.Repeat("a", maxConfigText+1) + `"`, "", "at most"},
		"toggle ok":      {on, `true`, "true", ""},
		"toggle bad":     {on, `"yes"`, "", "true or false"},
		"no type":        {Field{Key: "x"}, `"a"`, "", "type"},
	} {
		got, msg := CheckValue(c.f, json.RawMessage(c.raw))
		if c.bad != "" {
			if msg == "" || !strings.Contains(msg, c.bad) {
				t.Errorf("%s: %q is not %q", name, msg, c.bad)
			}
			continue
		}
		if msg != "" || got != c.want {
			t.Errorf("%s: %q %q, want %q", name, got, msg, c.want)
		}
	}
}

func TestRenderFillsPlaceholdersAndListsTheRest(t *testing.T) {
	m, _, err := func() (Manifest, string, error) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"manifest.json": configManifest, "prompt.txt": "Be {level} at {missing}."})
		return LoadDir(dir)
	}()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"terseLevel": "lite"}
	get := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	got := Render(m, "Be {level} at {missing}.", get)
	if !strings.HasPrefix(got, "Be lite at {missing}.") {
		t.Fatalf("placeholder not filled: %q", got)
	}
	if !strings.Contains(got, activeConfig+`- Scale: 3 (scale 1-10)`) {
		t.Fatalf("configuration block missing: %q", got)
	}
	if strings.Contains(got, "routing_mode") || strings.Contains(got, "Level:") {
		t.Fatalf("a filled or routing field was listed: %q", got)
	}
	if got := Render(m, "nothing here", nil); !strings.HasSuffix(got, "- Scale: 3 (scale 1-10)") {
		t.Fatalf("defaults not used: %q", got)
	}
	if got := Render(Manifest{}, "keep", nil); got != "keep" {
		t.Fatalf("a skill without fields rewrote the prompt: %q", got)
	}
	if got := Render(m, "", get); got != "" {
		t.Fatalf("an empty prompt grew configuration: %q", got)
	}
}

func TestFieldForFindsTheManifestKey(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"terse/manifest.json": configManifest, "terse/prompt.txt": "x"})
	r := NewRegistry(root, nil)
	if f, ok := r.FieldFor("terseLevel"); !ok || f.Type != "enum" {
		t.Fatalf("field %+v ok %v", f, ok)
	}
	if _, ok := r.FieldFor("terse_missing"); ok {
		t.Fatal("an undeclared key resolved")
	}
	var nilReg *Registry
	if _, ok := nilReg.FieldFor("x"); ok {
		t.Fatal("a nil registry resolved a key")
	}
}

func TestAFieldCannotReadACredentialIntoThePrompt(t *testing.T) {
	for _, key := range []string{"oidcClientSecret", "password", "mitmSudoEncrypted"} {
		manifest := `{"id":"leak","name":"Leak","description":"d","hook":"system-prompt","config_schema":[` +
			`{"key":"x","type":"text","legacy_key":"` + key + `"}]}`
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"manifest.json": manifest, "prompt.txt": "value {x}"})
		if _, _, err := LoadDir(dir); err == nil {
			t.Fatalf("a manifest reading %s loaded", key)
		}
	}
	// A value already stored under such a key is never rendered, even for a
	// manifest that got past loading some other way.
	m := Manifest{ID: "leak", Config: []Field{{Key: "x", Type: "text", LegacyKey: "oidcClientSecret"}}}
	got := Render(m, "value {x}", func(key string) (string, bool) { return "the-secret", true })
	if strings.Contains(got, "the-secret") {
		t.Fatalf("the secret reached the prompt: %q", got)
	}
}
