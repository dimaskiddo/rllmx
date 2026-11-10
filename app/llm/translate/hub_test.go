package translate

import "testing"

func TestMessageTextJoinsOnlyTheTextParts(t *testing.T) {
	img := Part{Type: PartImage}
	cases := []struct {
		name string
		in   []Part
		want string
	}{
		{"none", nil, ""},
		{"image only", []Part{img}, ""},
		{"one", []Part{{Type: PartText, Text: "a"}}, "a"},
		{"one among images", []Part{img, {Type: PartText, Text: "a"}, img}, "a"},
		{"two", []Part{{Type: PartText, Text: "a"}, {Type: PartText, Text: "b"}}, "ab"},
		{"two around an image", []Part{{Type: PartText, Text: "a"}, img, {Type: PartText, Text: "b"}}, "ab"},
		{"empty parts", []Part{{Type: PartText}, {Type: PartText, Text: "x"}, {Type: PartText}}, "x"},
	}
	for _, c := range cases {
		if got := (Message{Content: c.in}).Text(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
