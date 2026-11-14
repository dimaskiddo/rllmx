package veoaifree

import (
	"strings"
	"testing"
)

func FuzzImageParts(f *testing.F) {
	f.Add("https://cdn.example/a.png , https://cdn.example/b.png")
	f.Add(`"aGVsbG8="`)
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		urls, inline := imageParts(text)
		if len(urls)+len(inline) > maxParts {
			t.Fatalf("parts = %d", len(urls)+len(inline))
		}
		for _, u := range urls {
			if !strings.HasPrefix(u, "https://") {
				t.Fatalf("address = %q", u)
			}
		}
	})
}

func FuzzVideoURL(f *testing.F) {
	f.Add(`{"videoUrl":"https://cdn.example/one.mp4"}`)
	f.Add("nothing")
	f.Fuzz(func(t *testing.T, text string) {
		got := videoURL(text)
		if got != "" && (!strings.HasPrefix(got, "https://") || !strings.Contains(strings.ToLower(got), ".mp4")) {
			t.Fatalf("address = %q", got)
		}
	})
}
