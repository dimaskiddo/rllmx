package media

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	appfile "rllmx/app/platform/file"
)

// SameSite reports a poll address on the provider's own site: the same host, or
// a subdomain of the base address's host. A credential is never sent elsewhere.
func SameSite(base, target string) bool {
	b, err := url.Parse(base)
	if err != nil || b.Hostname() == "" {
		return false
	}
	t, err := url.Parse(target)
	if err != nil || t.Scheme != "https" {
		return false
	}
	host, want := strings.ToLower(t.Hostname()), strings.ToLower(b.Hostname())
	return host == want || strings.HasSuffix(host, "."+want)
}

// PollInterval and PollTimeout bound a provider that runs a job asynchronously:
// the reference polls every 1.5 s for up to two minutes.
const (
	PollInterval = 1500 * time.Millisecond
	PollTimeout  = 120 * time.Second
)

// Poll calls step until it reports done, an error, the request context ends or
// the two-minute deadline passes. It waits first, as the reference does.
func Poll(ctx context.Context, step func(context.Context) (bool, error)) error {
	deadline := time.NewTimer(PollTimeout)
	defer deadline.Stop()
	wait := time.NewTimer(PollInterval)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("the provider's job did not finish in time")
		case <-wait.C:
		}
		done, err := step(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		wait.Reset(PollInterval)
	}
}

// NowSec is the current time in Unix seconds, for an answer's created field.
func NowSec() int64 { return time.Now().Unix() }

// sizeRatios is the OpenAI size to aspect-ratio map the reference uses.
var sizeRatios = map[string]string{
	"1024x1024": "1:1",
	"1024x1792": "9:16",
	"1792x1024": "16:9",
	"1024x1536": "2:3",
	"1536x1024": "3:2",
}

// SizeToAspectRatio maps a requested size to a vendor aspect ratio, 1:1 for
// anything it does not know.
func SizeToAspectRatio(size string) string {
	if r, ok := sizeRatios[size]; ok {
		return r
	}
	return "1:1"
}

// SizeToDimensions reads "WxH" into its two numbers, zero when the value is not
// that shape.
func SizeToDimensions(size string) (width, height int) {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return 0, 0
	}
	wv, err := strconv.Atoi(strings.TrimSpace(w))
	if err != nil || wv <= 0 {
		return 0, 0
	}
	hv, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || hv <= 0 {
		return 0, 0
	}
	return wv, hv
}

// PixelRatio is a requested size as Runway's "W:H", 1024:1024 when unknown.
func PixelRatio(size string) string {
	w, h := SizeToDimensions(size)
	if w == 0 || h == 0 {
		return "1024:1024"
	}
	return strconv.Itoa(w) + ":" + strconv.Itoa(h)
}

// InlineImage turns a request's source image into raw base64: a data URL is
// stripped, a remote URL is fetched through the guarded client, anything else is
// taken as base64. A URL that cannot be fetched is refused, never sent on.
func InlineImage(ctx context.Context, c Client, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if i := strings.Index(raw, ";base64,"); strings.HasPrefix(raw, "data:image/") && i >= 0 {
		return raw[i+len(";base64,"):], nil
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return FetchImage(ctx, c, raw)
	}
	if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
		return "", BadRequestf("the source image is not base64")
	}
	return raw, nil
}

// FetchImage fetches an image URL through the guarded client and bounds it at
// 10 MiB, the same limit the request prefetch uses.
func FetchImage(ctx context.Context, c Client, target string) (string, error) {
	if c == nil {
		return "", errors.New("no client")
	}
	var status int
	var body []byte
	var err error
	const imageCap = 10 << 20
	if capped, ok := c.(interface {
		DoCapped(ctx context.Context, method, target string, header map[string]string, body []byte, limit int64) (int, http.Header, []byte, error)
	}); ok {
		status, _, body, err = capped.DoCapped(ctx, http.MethodGet, target, nil, nil, imageCap)
	} else {
		status, _, body, err = c.Do(ctx, http.MethodGet, target, nil, nil)
	}
	switch {
	case err != nil:
		return "", err
	}
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", &ProviderError{Status: status, Text: "the image could not be fetched"}
	}
	if len(body) > 10<<20 {
		return "", &ProviderError{Status: http.StatusBadGateway, Text: "the image is too large"}
	}
	if appfile.ImageMIME(body) == "" {
		return "", &ProviderError{Status: http.StatusBadGateway, Text: "the answer is not an image"}
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

// BinaryImage is an answer's image bytes as base64, for a provider that answers
// the image itself.
func BinaryImage(body []byte) (string, string) {
	mime := appfile.ImageMIME(body)
	if mime == "" {
		return "", ""
	}
	return base64.StdEncoding.EncodeToString(body), mime
}
