package upstream

import (
	"testing"
	"testing/fstest"
)

// The reference marks these providers as serving any id; the flag is what a
// catalog reads, so a data file that loses it changes the listing.
func TestThePassthroughProvidersAreMarked(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"commandcode", "opencode", "kimchi", "bedrock", "zed", "openrouter", "nous"} {
		e, ok := reg.ByID(id)
		if !ok {
			t.Fatalf("%s is missing", id)
		}
		if !e.PassthroughModels {
			t.Errorf("%s is not marked as serving any id", id)
		}
	}
	// A provider with a fixed catalog is not.
	for _, id := range []string{"openai", "claude", "deepseek"} {
		if e, ok := reg.ByID(id); ok && e.PassthroughModels {
			t.Errorf("%s was marked as serving any id", id)
		}
	}
}

func TestTheTimeoutAndRegionSchema(t *testing.T) {
	files := map[string]string{
		"p.json": `{"id":"p","category":"apikey","transport":{"baseUrl":"https://x.example/chat","timeoutMs":120000,"stallTimeoutMs":90000,
			"regions":[{"id":"sgp","host":"https://sgp.example"},{"id":"cn","host":"https://cn.example"}],"defaultRegion":"sgp"},"models":[]}`,
	}
	reg, err := Load(fstest.MapFS{"p.json": {Data: []byte(files["p.json"])}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := reg.ByID("p")
	if e.Transport.Timeout().Milliseconds() != 120000 || e.Transport.Stall().Milliseconds() != 90000 {
		t.Fatalf("timeouts = %v %v", e.Transport.Timeout(), e.Transport.Stall())
	}
	if got := e.Transport.RegionHost(""); got != "https://sgp.example" {
		t.Fatalf("default region host = %q", got)
	}
	if got := e.Transport.RegionHost("cn"); got != "https://cn.example" {
		t.Fatalf("cn region host = %q", got)
	}
	if got := e.Transport.RegionHost("nowhere"); got != "" {
		t.Fatalf("an unknown region resolved to %q", got)
	}
}

func TestBadTimeoutAndRegionValuesFailTheLoad(t *testing.T) {
	bad := map[string]string{
		"a negative timeout":          `"timeoutMs":-1`,
		"a huge timeout":              `"timeoutMs":999999999`,
		"a region without a host":     `"regions":[{"id":"sgp"}]`,
		"a region host with a path":   `"regions":[{"id":"sgp","host":"https://x.example/a"}]`,
		"a duplicate region":          `"regions":[{"id":"a","host":"https://a.example"},{"id":"a","host":"https://b.example"}]`,
		"a default region not listed": `"regions":[{"id":"a","host":"https://a.example"}],"defaultRegion":"b"`,
		"a region id with a space":    `"regions":[{"id":"a b","host":"https://a.example"}]`,
	}
	for name, extra := range bad {
		file := `{"id":"p","category":"apikey","transport":{"baseUrl":"https://x.example/chat",` + extra + `},"models":[]}`
		if _, err := Load(fstest.MapFS{"p.json": {Data: []byte(file)}}, "."); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A data file is a contract: an unknown key means the file and the code no
// longer agree, and reading it as if they did would silently drop the setting.
func TestAnUnknownKeyInADataFileIsRefused(t *testing.T) {
	const file = `{"id":"p","category":"apikey","display":{"name":"P"},"transport":{"baseUrl":"https://p.example/v1","format":"openai","noSuchKey":true},"models":[{"id":"m"}]}`
	if _, err := Load(fstest.MapFS{"p.json": {Data: []byte(file)}}, "."); err == nil {
		t.Fatal("a data file with an unknown key was accepted")
	}
}
