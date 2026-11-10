package zed

import "testing"

func TestTheCatalogKeepsTheModelsARequestCanCall(t *testing.T) {
	out := parseCatalog([]byte(`{"default_model":"m1","models":[
		{"id":"m1","display_name":"One","provider":"anthropic"},
		{"id":{"id":"m2"},"provider":"open_ai"},
		{"id":"off","provider":"google","is_disabled":true},
		{"id":"noprovider"}
	]}`))
	if out.Default != "m1" || len(out.Models) != 2 {
		t.Fatalf("%+v", out)
	}
	if out.Models["m2"].Provider != "open_ai" {
		t.Errorf("an object id was not read: %+v", out.Models)
	}
	if _, ok := out.Models["off"]; ok {
		t.Error("a disabled model was listed")
	}
}
