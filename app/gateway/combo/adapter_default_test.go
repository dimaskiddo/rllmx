package combo

import (
	"strings"
	"testing"

	"rllmx/app/llm/upstream"
)

// A pool with no models falls back to AdapterDefault, and a default this build
// cannot send leaves the pool empty and the adapter a silent no-op.
func TestTheDefaultAdapterModelIsOneThisBuildCanSend(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	provider, model, ok := strings.Cut(AdapterDefault, "/")
	if !ok {
		t.Fatalf("AdapterDefault %q names no model", AdapterDefault)
	}
	id, ok := reg.Resolve(provider)
	if !ok {
		t.Fatalf("the default adapter provider %q is not in the registry", provider)
	}
	if _, ok := reg.Model(id, model); !ok {
		t.Fatalf("%s is not a model of %s", model, id)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.For(id, model).Vision {
		t.Fatalf("%s does not read images, so its pool filters it out", AdapterDefault)
	}
}
