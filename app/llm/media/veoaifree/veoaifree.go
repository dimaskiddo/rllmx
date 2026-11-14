// Package veoaifree serves veoaifree.com: a WordPress AJAX endpoint behind a
// CSRF nonce, no credential, one scene-data poll for a video and a guarded
// download for the produced asset. The site rate-limits by address, so a 429 is
// returned rather than retried.
package veoaifree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	appfile "rllmx/app/platform/file"
)

const (
	maxArtifact = 100 << 20
	nonceBudget = 20 * time.Second
	jobBudget   = 30 * time.Minute
	pollEvery   = 20 * time.Second
	pollLimit   = 30
	readyRetry  = 5 * time.Second
	readyLimit  = 12
	// maxParts bounds the images one answer may name.
	maxParts = 8
)

// Images is the image adapter.
type Images struct{}

// Videos is the video adapter. Every is how long it waits between looks at a
// render; zero is the service's own pace, which only a test changes.
type Videos struct{ Every time.Duration }

// Kind is the service the image adapter serves.
func (Images) Kind() string { return upstream.KindImage }

// Kind is the service the video adapter serves.
func (Videos) Kind() string { return upstream.KindVideo }

// Direct runs one image generation.
func (Images) Direct(ctx context.Context, r media.Request, c media.Client) (media.Response, error) {
	return run(ctx, r, c, upstream.KindImage, 0)
}

// Direct runs one video generation.
func (v Videos) Direct(ctx context.Context, r media.Request, c media.Client) (media.Response, error) {
	return run(ctx, r, c, upstream.KindVideo, v.Every)
}

// run is the one path both services take.
func run(ctx context.Context, r media.Request, c media.Client, kind string, every time.Duration) (media.Response, error) {
	if c == nil {
		return media.Response{}, errors.New("veoaifree: no client")
	}
	body := gjson.ParseBytes(r.Body)
	prompt, err := media.WebPrompt(r.Body)
	if err != nil {
		return media.Response{}, err
	}
	base := media.BaseURL(r)
	if base == "" {
		return media.Response{}, errors.New("veoaifree: no address")
	}
	s := &session{base: base, every: every}
	nonce, err := s.nonce(ctx, c)
	if err != nil {
		return media.Response{}, err
	}
	aspect := aspectOf(body.Get("size").String())
	if kind == upstream.KindImage {
		return s.image(ctx, c, nonce, prompt, aspect)
	}
	return s.video(ctx, c, nonce, prompt, aspect)
}

// session keeps the per-call cookie jar the page hands out.
type session struct {
	base    string
	cookies []string
	// every is how long a poll waits between looks and a download between
	// retries; zero takes the service's own pace. A test sets it small.
	every time.Duration
}

// pace is the wait between looks at a render, or between reads of its file.
func (s *session) pace(own time.Duration) time.Duration {
	if s.every > 0 {
		return s.every
	}
	return own
}

// nonce reads the page and scrapes the request token.
func (s *session) nonce(ctx context.Context, c media.Client) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, nonceBudget)
	defer cancel()
	status, header, body, err := media.Capped(cctx, c, http.MethodGet, s.base+"/", map[string]string{"User-Agent": media.WebUserAgent}, nil, 2<<20)
	if err != nil {
		return "", err
	}
	s.absorb(header)
	if status < 200 || status > 299 {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The service did not answer"}
	}
	m := noncePattern.FindSubmatch(body)
	if len(m) != 2 {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The service's request token could not be read"}
	}
	return string(m[1]), nil
}

// image posts one image generation and reads its answer.
func (s *session) image(ctx context.Context, c media.Client, nonce, prompt, aspect string) (media.Response, error) {
	form := url.Values{
		"action":             {"veo_video_generator"},
		"nonce":              {nonce},
		"actionType":         {"banan-image-generator"},
		"promptIMG":          {prompt},
		"totalVariationsIMG": {"1"},
		"aspectRatioIMG":     {"IMAGE_ASPECT_RATIO_" + aspect},
	}
	text, err := s.post(ctx, c, form)
	if err != nil {
		return media.Response{}, err
	}
	urls, inline := imageParts(text)
	if len(urls) == 0 && len(inline) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "The service produced no image"}
	}
	data := make([]map[string]string, 0, len(urls)+len(inline))
	for _, u := range urls {
		data = append(data, map[string]string{"url": u})
	}
	for _, raw := range inline {
		data = append(data, map[string]string{"b64_json": base64.StdEncoding.EncodeToString(raw), "mime_type": appfile.ImageMIME(raw)})
	}
	out, err := json.Marshal(map[string]any{"created": media.NowSec(), "data": data})
	if err != nil {
		return media.Response{}, err
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

// video posts one video generation, polls its scene and reads the result.
func (s *session) video(ctx context.Context, c media.Client, nonce, prompt, aspect string) (media.Response, error) {
	form := url.Values{
		"action":          {"veo_video_generator"},
		"nonce":           {nonce},
		"actionType":      {"full-video-generate"},
		"prompt":          {prompt},
		"totalVariations": {"1"},
		"aspectRatio":     {"VIDEO_ASPECT_RATIO_" + aspect},
	}
	text, err := s.post(ctx, c, form)
	if err != nil {
		return media.Response{}, err
	}
	scene := gjson.Get(text, "sceneData").String()
	if scene == "" {
		return media.Response{}, media.BadRequestf("the request was refused")
	}
	target, err := s.await(ctx, c, scene)
	if err != nil {
		return media.Response{}, err
	}
	if err := s.save(ctx, c, target); err != nil {
		return media.Response{}, err
	}
	out, err := json.Marshal(map[string]any{
		"id": "video-veoaifree-" + fmt.Sprint(media.NowSec()), "status": "completed",
		"video": map[string]string{"url": target},
	})
	if err != nil {
		return media.Response{}, err
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

// await polls the scene until the service names a finished video.
func (s *session) await(ctx context.Context, c media.Client, scene string) (string, error) {
	var link string
	err := media.PollEvery(ctx, s.pace(pollEvery), min(jobBudget, pollEvery*pollLimit), func(ctx context.Context) (bool, error) {
		link = s.pollOnce(ctx, c, scene)
		return link != "", nil
	}, &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "The service did not finish the video in time"})
	if err != nil {
		return "", err
	}
	return link, nil
}

// pollOnce asks once whether the scene's video is ready.
func (s *session) pollOnce(ctx context.Context, c media.Client, scene string) string {
	cctx, cancel := context.WithTimeout(ctx, nonceBudget)
	defer cancel()
	text, err := s.post(cctx, c, url.Values{
		"action": {"veo_video_generator"}, "actionType": {"final-video-results"}, "sceneData": {scene},
	})
	if err != nil {
		return ""
	}
	link := videoURL(text)
	if link == "" {
		return ""
	}
	if err := media.HTTPSOnly(link); err != nil {
		return ""
	}
	return link
}

// save reads the asset once, retrying while the service reports it is not ready,
// and refuses anything that is not an mp4.
func (s *session) save(ctx context.Context, c media.Client, target string) error {
	if err := media.HTTPSOnly(target); err != nil {
		return err
	}
	for attempt := 0; attempt < readyLimit; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, nonceBudget)
		status, header, body, err := media.Capped(cctx, c, http.MethodGet, target, map[string]string{"Referer": s.base + "/", "User-Agent": media.WebUserAgent}, nil, maxArtifact)
		cancel()
		if err == nil && status >= 200 && status <= 299 && len(body) > 12 {
			mime := strings.Split(header.Get("Content-Type"), ";")[0]
			if !strings.HasPrefix(mime, "video/") && mime != "application/octet-stream" {
				return &media.ProviderError{Status: http.StatusBadGateway, Text: "The service's result is not a video"}
			}
			if string(body[4:8]) != "ftyp" {
				return &media.ProviderError{Status: http.StatusBadGateway, Text: "The service's result is not an mp4"}
			}
			return nil
		}
		if err == nil && !retryable(status) {
			return &media.ProviderError{Status: http.StatusBadGateway, Text: "The service's result could not be read"}
		}
		if err := media.Sleep(ctx, s.pace(readyRetry)); err != nil {
			return err
		}
	}
	return &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "The service's result did not become available in time"}
}

// post sends one AJAX form and returns its text.
func (s *session) post(ctx context.Context, c media.Client, form url.Values) (string, error) {
	h := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Origin": s.base,
		"Referer": s.base + "/", "User-Agent": media.WebUserAgent,
	}
	if len(s.cookies) > 0 {
		h["Cookie"] = strings.Join(s.cookies, "; ")
	}
	status, header, body, err := media.Capped(ctx, c, http.MethodPost, s.base+"/wp-admin/admin-ajax.php", h, []byte(form.Encode()), 8<<20)
	if err != nil {
		return "", err
	}
	s.absorb(header)
	switch {
	case status >= 200 && status <= 299:
		return string(body), nil
	case status == http.StatusTooManyRequests:
		return "", &media.ProviderError{Status: status, Text: "The service's free quota is spent for this address"}
	case status >= 400 && status < 500:
		return "", media.BadRequestf("the request was refused")
	}
	return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The service did not answer"}
}

// absorb keeps the cookies a response set, without repeating a name.
func (s *session) absorb(h http.Header) {
	if h == nil {
		return
	}
	for _, line := range h.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(line, ";")
		pair = strings.TrimSpace(pair)
		name, _, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			continue
		}
		kept := s.cookies[:0]
		for _, old := range s.cookies {
			if n, _, _ := strings.Cut(old, "="); n != name {
				kept = append(kept, old)
			}
		}
		s.cookies = append(kept, pair)
	}
}

// imageParts reads the image answer: comma-separated addresses, or inline
// base64.
func imageParts(text string) ([]string, [][]byte) {
	var urls []string
	var inline [][]byte
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		text = firstString(text)
	}
	for _, part := range strings.Split(text, ",") {
		if len(urls)+len(inline) >= maxParts {
			break
		}
		part = strings.TrimSpace(strings.Trim(part, `"`))
		switch {
		case part == "":
		case strings.HasPrefix(part, "https://"):
			urls = append(urls, part)
		case strings.HasPrefix(part, "data:image/"):
			if i := strings.Index(part, ";base64,"); i > 0 {
				if raw, err := base64.StdEncoding.DecodeString(part[i+len(";base64,"):]); err == nil && appfile.ImageMIME(raw) != "" {
					inline = append(inline, raw)
				}
			}
		default:
			if raw, err := base64.StdEncoding.DecodeString(part); err == nil && appfile.ImageMIME(raw) != "" {
				inline = append(inline, raw)
			}
		}
	}
	return urls, inline
}

// firstString reads the first string a JSON answer carries.
func firstString(text string) string {
	if v := gjson.Get(text, "url"); v.Exists() {
		return v.String()
	}
	if v := gjson.Get(text, "data.0.url"); v.Exists() {
		return v.String()
	}
	if v := gjson.Get(text, "data.0.b64_json"); v.Exists() {
		return v.String()
	}
	if v := gjson.Get(text, "0"); v.Exists() {
		return v.String()
	}
	return text
}

// videoURL finds the mp4 address a poll answer names.
func videoURL(text string) string {
	for _, key := range []string{"videoUrl", "video_url", "url", "data.videoUrl", "data.url"} {
		if v := gjson.Get(text, key).String(); strings.HasPrefix(v, "https://") && strings.Contains(v, ".mp4") {
			return v
		}
	}
	lower := strings.ToLower(text)
	if i := strings.Index(lower, "https://"); i >= 0 && i < len(text) && strings.HasPrefix(text[i:], "https://") {
		// ToLower can change a byte count, so the offset is re-checked on the
		// original text rather than trusted.
		rest := text[i:]
		if end := strings.IndexAny(rest, `"'\`+"`"+` ,<>`); end > 0 {
			rest = rest[:end]
		}
		if strings.Contains(strings.ToLower(rest), ".mp4") {
			return rest
		}
	}
	return ""
}

// aspectOf maps a size to the service's orientation word.
func aspectOf(size string) string {
	w, h := media.SizeToDimensions(size)
	switch {
	case w > 0 && h > 0 && w > h:
		return "LANDSCAPE"
	case w > 0 && h > 0 && w < h:
		return "PORTRAIT"
	}
	return "SQUARE"
}

// retryable reports a status that means the asset is still being produced.
func retryable(status int) bool {
	switch status {
	case http.StatusAccepted, http.StatusNotFound, http.StatusConflict, http.StatusTooEarly:
		return true
	}
	return false
}

// noncePattern reads the page's request token.
var noncePattern = regexp.MustCompile(`nonce":"([a-f0-9]+)"`)

var _ media.Director = Images{}
var _ media.Director = Videos{}
