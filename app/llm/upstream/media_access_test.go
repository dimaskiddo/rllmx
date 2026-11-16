package upstream

import "testing"

// allowRegistry is one image vendor, one search vendor and one speech vendor.
func allowRegistry(t *testing.T) *Registry {
	t.Helper()
	return loadMedia(t, map[string]string{
		"openai": `{"id":"openai","alias":"oai","category":"apikey","display":{"name":"O"},
			"media":{"serviceKinds":["image","tts"],"imageConfig":{"baseUrl":"https://api.openai.com/v1/images/generations",
			"models":[{"id":"gpt-image-1"},{"id":"gpt-image-2"}]},"ttsConfig":{"baseUrl":"https://api.openai.com/v1/audio/speech",
			"models":[{"id":"tts-1"}]}}}`,
		"serper": `{"id":"serper","category":"apikey","display":{"name":"S"},
			"media":{"serviceKinds":["webSearch"],"searchConfig":{"baseUrl":"https://google.serper.dev/search"}}}`,
	})
}

func TestAMediaModelIsAllowedAsProviderModelOrAsItsBareId(t *testing.T) {
	reg := allowRegistry(t)
	ref := MediaRef{Provider: "openai", Kind: KindImage, Model: "gpt-image-1"}
	for name, list := range map[string][]string{
		"provider/model": {"openai/gpt-image-1"},
		"the bare id":    {"gpt-image-1"},
		"the alias":      {"oai/gpt-image-1"},
		"among others":   {"serper", "openai/gpt-image-1"},
	} {
		if !reg.Allows(list, ref) {
			t.Errorf("%s: %v was refused", name, list)
		}
	}
}

func TestAMediaModelThatIsNotListedIsRefused(t *testing.T) {
	reg := allowRegistry(t)
	ref := MediaRef{Provider: "openai", Kind: KindImage, Model: "gpt-image-1"}
	for name, list := range map[string][]string{
		"another model":   {"openai/tts-1"},
		"another vendor":  {"serper"},
		"the bare vendor": {"openai"},
		"nothing":         {},
	} {
		if reg.Allows(list, ref) {
			t.Errorf("%s: %v was allowed", name, list)
		}
	}
}

func TestASpeechVoiceNamedAfterTheSlashIsMatchedAsTheRouteNamesIt(t *testing.T) {
	reg := allowRegistry(t)
	// Speech resolves `provider/<name>` where the name is the voice, so the list
	// must carry that same string.
	voice := MediaRef{Provider: "openai", Kind: KindTTS, Model: "alloy"}
	if !reg.Allows([]string{"openai/alloy"}, voice) {
		t.Fatal("the voice's own identity was refused")
	}
	if reg.Allows([]string{"openai/tts-1"}, voice) {
		t.Fatal("the model id allowed the voice")
	}
}

func TestAProviderOnlyRouteNeedsABareEntry(t *testing.T) {
	reg := allowRegistry(t)
	search := MediaRef{Provider: "serper", Kind: KindSearch}
	if !reg.Allows([]string{"serper"}, search) {
		t.Fatal("a bare provider entry was refused")
	}
	if reg.Allows([]string{"serper/serper"}, search) {
		t.Fatal("an entry with a slash opened a provider-only route")
	}
	if reg.Allows([]string{"openai"}, search) {
		t.Fatal("another provider's entry opened it")
	}
}

func TestTheComparisonIsExact(t *testing.T) {
	reg := allowRegistry(t)
	if reg.Allows([]string{"openai/GPT-Image-1"}, MediaRef{Provider: "openai", Kind: KindImage, Model: "gpt-image-1"}) {
		t.Fatal("a differently cased model matched")
	}
	if reg.Allows([]string{"Serper"}, MediaRef{Provider: "serper", Kind: KindSearch}) {
		t.Fatal("a differently cased provider matched")
	}
}

func TestABareModelEntryDoesNotOpenItsProvider(t *testing.T) {
	reg := allowRegistry(t)
	if !reg.Allows([]string{"gpt-image-1"}, MediaRef{Provider: "openai", Kind: KindImage, Model: "gpt-image-1"}) {
		t.Fatal("the bare id was refused")
	}
	if reg.Allows([]string{"gpt-image-1"}, MediaRef{Provider: "openai", Kind: KindImage, Model: "gpt-image-2"}) {
		t.Fatal("a bare model entry allowed a different model of the same provider")
	}
}
