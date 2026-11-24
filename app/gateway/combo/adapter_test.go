package combo

import (
	"testing"

	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

func adapterDoc(t *testing.T, raw string) document.Doc {
	t.Helper()
	doc, err := document.ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestAdaptersForReadsTheStoredPool(t *testing.T) {
	doc := adapterDoc(t, `{"capacityAdapter":{"vision":{"enabled":true,"roundRobin":true,"models":["a/one","b/two"]},`+
		`"audioInput":{"enabled":true},"pdf":{"enabled":false,"models":["c/three"]},"bogus":{"enabled":true}}}`)
	pools := AdaptersFor(doc)
	if len(pools) != 2 {
		t.Fatalf("%+v", pools)
	}
	if v := pools["vision"]; !v.Enabled || !v.RoundRobin || len(v.Models) != 2 {
		t.Fatalf("vision %+v", v)
	}
	if a := pools["audioInput"]; len(a.Models) != 1 || a.Models[0] != AdapterDefault {
		t.Fatalf("an enabled empty pool reads %+v", a)
	}
	if _, ok := pools["pdf"]; ok {
		t.Fatal("a disabled pool was read")
	}
	if AdaptersFor(adapterDoc(t, `{}`)) != nil {
		t.Fatal("a missing row read as a pool")
	}
}

func TestAdapterKindsFollowTheTurn(t *testing.T) {
	if got := AdapterKinds(translate.Needs{}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	got := AdapterKinds(translate.Needs{Vision: true, Audio: true})
	if len(got) != 2 || got[0] != "vision" || got[1] != "audioInput" {
		t.Fatalf("%v", got)
	}
	if !KindMet("vision", upstream.Capability{Vision: true}) || KindMet("pdf", upstream.Capability{Vision: true}) {
		t.Fatal("KindMet does not read the row's field")
	}
	if !KindMet("audioInput", upstream.Capability{AudioInput: true}) || !KindMet("videoInput", upstream.Capability{VideoInput: true}) {
		t.Fatal("an audio or video row is not read")
	}
}
