// Package prefetch turns remote image URLs in a request into inline data, for
// upstreams that cannot fetch a URL themselves.
package prefetch

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/file"
	"rllmx/app/platform/network"
)

// Limits of one request.
const (
	MaxImages   = 16
	MaxTotal    = 20 << 20
	MaxEach     = 10 << 20
	fetchTimout = 10 * time.Second
	clientTime  = 20 * time.Second
	redirects   = 3
)

// Fetcher downloads images through a guarded client. A nil Client fetches nothing.
type Fetcher struct{ Client *http.Client }

// New returns a Fetcher whose every dial, and every redirect hop, goes through
// the outbound guard.
func New() *Fetcher {
	c := network.NewClient(network.Options{ConnectTimeout: fetchTimout, MaxRedirects: redirects})
	c.Timeout = clientTime
	return &Fetcher{Client: c}
}

type fetched struct {
	media string
	data  string
}

// Inline replaces the URL of each remote image part with its bytes and returns
// how many parts it changed. A part whose fetch fails keeps its URL, so the
// request still goes out. It stops when ctx ends.
func (f *Fetcher) Inline(ctx context.Context, req *translate.Request) int {
	if f == nil || f.Client == nil || req == nil {
		return 0
	}
	seen := map[string]*fetched{}
	var total, tried, changed int
	for i := range req.Messages {
		parts := req.Messages[i].Content
		for j := range parts {
			p := &parts[j]
			if p.Type != translate.PartImage || p.Data != "" || !remote(p.URL) {
				continue
			}
			got, done := seen[p.URL]
			if !done {
				if ctx.Err() != nil {
					return changed
				}
				if tried >= MaxImages {
					continue
				}
				tried++
				got = f.fetch(ctx, p.URL, MaxTotal-total)
				seen[p.URL] = got
				if got != nil {
					total += base64.StdEncoding.DecodedLen(len(got.data))
				}
			}
			if got == nil {
				continue
			}
			p.URL, p.Data, p.MediaType = "", got.data, got.media
			changed++
		}
	}
	return changed
}

func remote(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func (f *Fetcher) fetch(ctx context.Context, url string, room int) *fetched {
	limit := min(MaxEach, room)
	if limit <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > int64(limit) {
		return nil
	}
	raw, err := file.ReadCapped(resp.Body, int64(limit))
	if err != nil {
		return nil
	}
	media := file.ImageMIME(raw)
	if media == "" {
		return nil
	}
	return &fetched{media: media, data: base64.StdEncoding.EncodeToString(raw)}
}
