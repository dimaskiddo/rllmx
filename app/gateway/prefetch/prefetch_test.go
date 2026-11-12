package prefetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"rllmx/app/platform/file"
	"strconv"
	"sync/atomic"
	"testing"

	"rllmx/app/llm/translate"
)

var (
	png  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...)
	jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 20)...)
)

func request(urls ...string) *translate.Request {
	var parts []translate.Part
	for _, u := range urls {
		parts = append(parts, translate.Part{Type: translate.PartImage, URL: u})
	}
	return &translate.Request{Messages: []translate.Message{{Role: translate.RoleUser, Content: parts}}}
}

func part(r *translate.Request, i int) translate.Part { return r.Messages[0].Content[i] }

func server(t *testing.T, h http.HandlerFunc) (*Fetcher, string) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Fetcher{Client: s.Client()}, s.URL
}

func TestAnImageIsInlinedFromItsBytesNotItsHeader(t *testing.T) {
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if r.URL.Path == "/j" {
			w.Write(jpeg)
			return
		}
		w.Write(png)
	})
	r := request(base+"/p", base+"/j")
	if n := f.Inline(context.Background(), r); n != 2 {
		t.Fatalf("changed %d", n)
	}
	p, j := part(r, 0), part(r, 1)
	if p.URL != "" || p.MediaType != "image/png" || p.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("png: %+v", p)
	}
	if j.MediaType != "image/jpeg" {
		t.Fatalf("jpeg: %+v", j)
	}
}

func TestEveryKnownMagic(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), 1)
	cases := map[string][]byte{
		"image/gif":  []byte("GIF89a...."),
		"image/webp": webp,
		"image/bmp":  []byte("BM...."),
	}
	for want, b := range cases {
		if got := file.ImageMIME(b); got != want {
			t.Errorf("%s: got %q", want, got)
		}
	}
	if file.ImageMIME([]byte("<html>")) != "" || file.ImageMIME(nil) != "" {
		t.Fatal("a non-image was accepted")
	}
}

func TestAFailureKeepsTheURL(t *testing.T) {
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404":
			w.WriteHeader(http.StatusNotFound)
			w.Write(png)
		case "/page":
			w.Write([]byte("<html>not an image</html>"))
		case "/big":
			w.Write(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxEach)...))
		}
	})
	r := request(base+"/404", base+"/page", base+"/big")
	if n := f.Inline(context.Background(), r); n != 0 {
		t.Fatalf("changed %d", n)
	}
	for i := range 3 {
		if p := part(r, i); p.URL == "" || p.Data != "" {
			t.Fatalf("part %d lost its URL: %+v", i, p)
		}
	}
}

func TestOnlyRemoteImagesWithoutDataAreTouched(t *testing.T) {
	var hits atomic.Int32
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) })
	r := request("data:image/png;base64,AAAA", "ftp://x/y.png", base+"/a")
	r.Messages[0].Content = append(r.Messages[0].Content,
		translate.Part{Type: translate.PartImage, URL: base + "/b", Data: "keep"},
		translate.Part{Type: translate.PartText, Text: base + "/c"})
	if n := f.Inline(context.Background(), r); n != 1 || hits.Load() != 1 {
		t.Fatalf("changed %d, fetched %d", n, hits.Load())
	}
	if part(r, 0).URL != "data:image/png;base64,AAAA" || part(r, 3).Data != "keep" {
		t.Fatal("a part that is not remote was changed")
	}
}

func TestTheSameURLIsFetchedOnce(t *testing.T) {
	var hits atomic.Int32
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) })
	r := request(base+"/a", base+"/a", base+"/a")
	if n := f.Inline(context.Background(), r); n != 3 || hits.Load() != 1 {
		t.Fatalf("changed %d, fetched %d", n, hits.Load())
	}
}

func TestAtMostSixteenImagesAreFetched(t *testing.T) {
	var hits atomic.Int32
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) })
	var urls []string
	for i := range MaxImages + 4 {
		urls = append(urls, base+"/"+strconv.Itoa(i))
	}
	r := request(urls...)
	if n := f.Inline(context.Background(), r); n != MaxImages || int(hits.Load()) != MaxImages {
		t.Fatalf("changed %d, fetched %d", n, hits.Load())
	}
	if part(r, MaxImages).URL == "" {
		t.Fatal("an image past the cap was inlined")
	}
}

func TestTheTotalIsCapped(t *testing.T) {
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxEach-100)...)
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) { w.Write(big) })
	r := request(base+"/a", base+"/b", base+"/c")
	n := f.Inline(context.Background(), r)
	if n != 2 {
		t.Fatalf("changed %d, want 2 (two fit in %d bytes, the third does not)", n, MaxTotal)
	}
	if part(r, 2).URL == "" {
		t.Fatal("the third image was inlined past the total")
	}
}

func TestACancelledContextFetchesNothing(t *testing.T) {
	var hits atomic.Int32
	f, base := server(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := f.Inline(ctx, request(base+"/a")); n != 0 || hits.Load() != 0 {
		t.Fatalf("changed %d, fetched %d", n, hits.Load())
	}
}

func TestZeroValuesFetchNothing(t *testing.T) {
	var f *Fetcher
	if f.Inline(context.Background(), request("http://x/y")) != 0 {
		t.Fatal("nil fetcher")
	}
	if (&Fetcher{}).Inline(context.Background(), request("http://x/y")) != 0 {
		t.Fatal("no client")
	}
	if New().Inline(context.Background(), nil) != 0 {
		t.Fatal("nil request")
	}
}

func TestTheDefaultFetcherRefusesALoopbackAddress(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(png) }))
	defer s.Close()
	r := request(s.URL + "/a")
	if n := New().Inline(context.Background(), r); n != 0 || hits.Load() != 0 {
		t.Fatalf("changed %d, reached the server %d times", n, hits.Load())
	}
}

func FuzzInline(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"), "http://x/y")
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBP"), "https://x/\x00")
	f.Add([]byte{}, "")
	f.Fuzz(func(t *testing.T, body []byte, url string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
		defer srv.Close()
		fe := &Fetcher{Client: srv.Client()}
		r := request(srv.URL+"/a", url)
		fe.Inline(context.Background(), r)
		for _, p := range r.Messages[0].Content {
			if p.Data != "" && p.URL != "" {
				t.Fatalf("both data and url: %+v", p)
			}
			if p.Data != "" && file.ImageMIME(mustDecode(t, p.Data)) != p.MediaType {
				t.Fatalf("media type %q does not match the bytes", p.MediaType)
			}
		}
	})
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type countingTransport struct{ hits atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.hits.Add(1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(png)), Header: http.Header{}}, nil
}

func TestACancelledContextNeverReachesTheTransport(t *testing.T) {
	tr := &countingTransport{}
	f := &Fetcher{Client: &http.Client{Transport: tr}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := f.Inline(ctx, request("https://a.example/1", "https://a.example/2")); n != 0 || tr.hits.Load() != 0 {
		t.Fatalf("changed %d, transport hit %d times", n, tr.hits.Load())
	}
}
