package chatgptweb

import "testing"

func FuzzScrape(f *testing.F) {
	f.Add([]byte(`{"conversation_id":"0b9a5c1e-1111-2222-3333-444455556666","a":"file-service://x"}`))
	f.Add([]byte(`"sediment://one"`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, files, sediments := scrape(data)
		if len(files) > maxIDs || len(sediments) > maxIDs {
			t.Fatalf("ids = %d files, %d sediments", len(files), len(sediments))
		}
		for _, id := range append(append([]string{}, files...), sediments...) {
			if id == "" || id == "file_upload" {
				t.Fatalf("scrape kept %q", id)
			}
		}
	})
}
