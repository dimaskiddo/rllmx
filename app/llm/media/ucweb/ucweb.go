// Package ucweb serves the uncensored.com web provider: a Clerk session minted
// from the pasted cookie, one internal call that names a result address, a HEAD
// poll until the asset exists, then a download. The provider rotates its client
// cookie per call, so the fresh value is handed back to the pipeline.
package ucweb

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/oauth"

	"rllmx/app/llm/upstream"
	appfile "rllmx/app/platform/file"
)

const (
	origin      = "https://uncensored.com"
	clerkAPI    = "https://clerk.uncensored.com"
	clerkJS     = "5.127.1"
	maxImage    = 100 << 20
	maxVideo    = 100 << 20
	mintBudget  = 20 * time.Second
	createWait  = 30 * time.Second
	pollEvery   = 5 * time.Second
	pollCeiling = 300 * time.Second
)

// aspectSizes are the ratios the image endpoint accepts, as pixel dimensions.
var aspectSizes = map[string][2]string{
	"1:1": {"1024", "1024"}, "16:9": {"1024", "576"}, "9:16": {"576", "1024"},
	"4:3": {"1024", "768"}, "3:4": {"768", "1024"}, "3:2": {"1152", "768"},
	"2:3": {"768", "1152"}, "4:5": {"896", "1120"}, "5:4": {"1120", "896"},
	"21:9": {"1344", "576"},
}

// Images is the image adapter. Every is how long it waits between looks at the
// result; zero is the provider's own pace, which only a test changes.
type Images struct{ Every time.Duration }

// Videos is the video adapter. Every is how long it waits between looks at the
// result; zero is the provider's own pace, which only a test changes.
type Videos struct{ Every time.Duration }

// Kind is the service the image adapter serves.
func (Images) Kind() string { return upstream.KindImage }

// Kind is the service the video adapter serves.
func (Videos) Kind() string { return upstream.KindVideo }

// Direct runs one image generation.
func (i Images) Direct(ctx context.Context, r media.Request, c media.Client) (media.Response, error) {
	return run(ctx, r, c, upstream.KindImage, i.Every)
}

// Direct runs one video generation.
func (v Videos) Direct(ctx context.Context, r media.Request, c media.Client) (media.Response, error) {
	return run(ctx, r, c, upstream.KindVideo, v.Every)
}

// run is the one path both services take.
func run(ctx context.Context, r media.Request, c media.Client, kind string, every time.Duration) (media.Response, error) {
	if c == nil {
		return media.Response{}, errors.New("uc-web: no client")
	}
	body := gjson.ParseBytes(r.Body)
	prompt, err := media.WebPrompt(r.Body)
	if err != nil {
		return media.Response{}, err
	}
	jar, err := media.ParseCookie(r.Credential)
	if err != nil {
		return media.Response{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored web cookie is not one this program sends"}
	}
	if jar.Get("__client") == "" {
		return media.Response{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored cookie has no client session"}
	}
	mint, err := mintSession(ctx, c, jar)
	if err != nil {
		return media.Response{}, err
	}
	mint.every = every
	ans := media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}}
	if mint.rotated != "" {
		if err := jar.Set("__client", mint.rotated); err == nil {
			ans.NewCredential = jar.Header()
		}
	}
	// A failure after the mint still hands the rotated cookie back: the old one
	// is already retired, so losing the new one would lock the account out.
	produce := video
	if kind == upstream.KindImage {
		produce = image
	}
	raw, err := produce(ctx, c, r, body, prompt, mint)
	if err != nil {
		return media.Response{NewCredential: ans.NewCredential}, err
	}
	ans.Body = raw
	return ans, nil
}

// session is what the Clerk mint returns.
type session struct {
	jwt     string
	uid     string
	rotated string
	// every is the wait between looks at a result; zero takes pollEvery.
	every time.Duration
}

// mintSession trades the durable client cookie for a short-lived session token.
func mintSession(ctx context.Context, c media.Client, jar *media.CookieJar) (session, error) {
	cctx, cancel := context.WithTimeout(ctx, mintBudget)
	defer cancel()
	headers := map[string]string{
		"Accept": "application/json", "Cookie": jar.Header(),
		"Origin": origin, "Referer": origin + "/", "User-Agent": media.WebUserAgent,
	}
	sid := claim(jar.Get("__client"), "sid")
	rotated := ""
	if sid == "" {
		status, header, body, err := media.Capped(cctx, c, http.MethodGet, clerkAPI+"/v1/client?_clerk_js_version="+clerkJS, headers, nil, 1<<20)
		if err != nil {
			return session{}, err
		}
		if status < 200 || status > 299 {
			return session{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored cookie has no active session"}
		}
		rotated = setCookie(header, "__client")
		payload := body
		if inner := gjson.GetBytes(body, "response"); inner.Exists() {
			payload = []byte(inner.Raw)
		}
		first := ""
		for _, s := range gjson.GetBytes(payload, "sessions").Array() {
			id := s.Get("id").String()
			if id == "" {
				continue
			}
			if s.Get("status").String() == "active" {
				sid = id
				break
			}
			if first == "" {
				first = id
			}
		}
		if sid == "" {
			sid = first
		}
	}
	if sid == "" {
		return session{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored cookie has no active session"}
	}
	status, header, body, err := media.Capped(cctx, c, http.MethodPost,
		clerkAPI+"/v1/client/sessions/"+url.PathEscape(sid)+"/tokens?_clerk_js_version="+clerkJS,
		map[string]string{
			"Content-Type": "application/x-www-form-urlencoded", "Cookie": jar.Header(),
			"Origin": origin, "Referer": origin + "/", "User-Agent": media.WebUserAgent,
		}, []byte{}, 1<<20)
	if err != nil {
		return session{}, err
	}
	if status < 200 || status > 299 {
		return session{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored cookie could not mint a session"}
	}
	if v := setCookie(header, "__client"); v != "" {
		rotated = v
	}
	jwt := gjson.GetBytes(body, "jwt").String()
	if jwt == "" {
		return session{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider minted no session token"}
	}
	return session{jwt: jwt, uid: claim(jwt, "uid"), rotated: rotated}, nil
}

// image posts one image generation and reads its result.
func image(ctx context.Context, c media.Client, r media.Request, body gjson.Result, prompt string, s session) ([]byte, error) {
	wire := wireModel(r)
	if wire == "" {
		return nil, media.BadRequestf("the request names no model")
	}
	aspect, width, height := aspectFor(body.Get("size").String())
	payload := map[string]any{
		"prompt": prompt, "mode": "dev", "model_version": wire, "m_n_user": true,
		"moderationMode": "SUPER_LIGHT", "country": "US", "vdiscount": false,
		"aspect_ratio": aspect, "imageWidth": width, "imageHeight": height,
	}
	if res := pick(body.Get("resolution").String(), []string{"1k", "1.5k", "2k", "4k", "8k"}); res != "" {
		payload["resolution"] = res
	}
	target, err := create(ctx, c, s, base(r)+"/v2/image-gen", payload)
	if err != nil {
		return nil, err
	}
	raw, mime, err := await(ctx, c, target, maxImage, s.every)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(map[string]any{
		"created": media.NowSec(),
		"data":    []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(raw), "mime_type": mime}},
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// video posts one video generation and reads its result.
func video(ctx context.Context, c media.Client, r media.Request, body gjson.Result, prompt string, s session) ([]byte, error) {
	wire := wireModel(r)
	if wire == "" {
		return nil, media.BadRequestf("the request names no model")
	}
	seconds := secondsOf(body, 5)
	res := pick(body.Get("resolution").String(), []string{"480p", "720p"})
	if res == "" {
		res = "480p"
	}
	payload := map[string]any{
		"prompt": prompt, "media_blob_name": nil, "num_frames": seconds*16 + 1,
		"frames_per_second": 16, "num_inference_steps": 30, "guide_scale": 5, "shift": 5,
		"aspect_ratio": "auto", "pro_mode": false, "turbo": wire == "turbo",
		"resolution": res, "sora_resolution": res, "end_frame_blob_name": nil,
		"model": wire, "seconds": seconds, "video_to_video_duration": seconds, "vdiscount": false,
	}
	if src := source(body); src != "" {
		blob, err := upload(ctx, c, s, src)
		if err != nil {
			return nil, err
		}
		payload["media_blob_name"] = blob
	}
	target, err := create(ctx, c, s, base(r)+"/image_to_video", payload)
	if err != nil {
		return nil, err
	}
	_, mime, err := await(ctx, c, target, maxVideo, s.every)
	if err != nil {
		return nil, err
	}
	if mime != "video/mp4" {
		return nil, &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider's video is not an mp4"}
	}
	out, err := json.Marshal(map[string]any{
		"id": "video-uc-web-" + fmt.Sprint(media.NowSec()), "status": "completed",
		"video": map[string]string{"url": target},
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// create posts one generation and returns the result address it names.
func create(ctx context.Context, c media.Client, s session, target string, payload map[string]any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, createWait)
	defer cancel()
	status, _, body, err := media.Capped(cctx, c, http.MethodPost, target, authHeaders(s.jwt), raw, 1<<20)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", &media.ProviderError{Status: status, Text: "The provider rejected the session"}
	}
	if status == http.StatusTooManyRequests {
		return "", &media.ProviderError{Status: status, Text: "The provider's quota is spent"}
	}
	if status >= 500 {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider did not answer"}
	}
	if status < 200 || status > 299 {
		return "", media.BadRequestf("the request was refused")
	}
	link := gjson.GetBytes(body, "url").String()
	if link == "" {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider named no result address"}
	}
	if err := media.HTTPSOnly(link); err != nil {
		return "", err
	}
	return link, nil
}

// upload stores a source image and returns its blob name.
func upload(ctx context.Context, c media.Client, s session, src string) (string, error) {
	if s.uid == "" {
		return "", &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored cookie carries no account id"}
	}
	b64, err := media.InlineImage(ctx, c, src)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		return "", media.BadRequestf("the source image is not base64")
	}
	payload, err := json.Marshal(map[string]string{"content_type": "image/png", "user_identifier": s.uid})
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, createWait)
	defer cancel()
	status, _, body, err := media.Capped(cctx, c, http.MethodPost, "https://internal-6.pubyar.com/generate-signed-url", authHeaders(s.jwt), payload, 1<<20)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", &media.ProviderError{Status: status, Text: "The provider refused the upload"}
	}
	signed := gjson.GetBytes(body, "signed_url").String()
	blob := gjson.GetBytes(body, "blob_name").String()
	if signed == "" || blob == "" {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider named no upload address"}
	}
	if err := media.HTTPSOnly(signed); err != nil {
		return "", err
	}
	status, _, _, err = media.Capped(cctx, c, http.MethodPut, signed, map[string]string{"Content-Type": "image/png"}, raw, 1<<20)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", &media.ProviderError{Status: status, Text: "The provider refused the upload"}
	}
	return blob, nil
}

// await polls the result address until it serves the asset, then reads it.
func await(ctx context.Context, c media.Client, target string, limit int64, every time.Duration) ([]byte, string, error) {
	if err := media.HTTPSOnly(target); err != nil {
		return nil, "", err
	}
	// The first look comes after one wait, as the provider's own client does.
	err := media.PollEvery(ctx, cmp.Or(every, pollEvery), pollCeiling, func(ctx context.Context) (bool, error) {
		cctx, cancel := context.WithTimeout(ctx, createWait)
		defer cancel()
		status, _, _, err := media.Capped(cctx, c, http.MethodHead, target, map[string]string{"Referer": origin + "/", "User-Agent": media.WebUserAgent}, nil, 1)
		return err == nil && status >= 200 && status < 300, nil
	}, &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "The provider's result did not become available in time"})
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	status, header, body, err := media.Capped(cctx, c, http.MethodGet, target, map[string]string{"Referer": origin + "/", "User-Agent": media.WebUserAgent}, nil, limit)
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status > 299 {
		return nil, "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider's result could not be read"}
	}
	mime := ""
	if m := appfile.ImageMIME(body); m != "" {
		mime = m
	} else if len(body) > 12 && string(body[4:8]) == "ftyp" {
		mime = "video/mp4"
	} else {
		mime = strings.Split(header.Get("Content-Type"), ";")[0]
	}
	if mime == "" || mime == "application/octet-stream" || strings.Contains(mime, "text/") {
		return nil, "", &media.ProviderError{Status: http.StatusBadGateway, Text: "The provider's result is not media"}
	}
	return body, mime, nil
}

// authHeaders are the internal calls' headers.
func authHeaders(jwt string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + jwt, "Origin": origin, "Referer": origin + "/",
		"Content-Type": "application/json", "User-Agent": media.WebUserAgent,
	}
}

// base is the service address from the provider's config.
func base(r media.Request) string {
	return media.BaseURL(r)
}

// wireModel is the model the provider knows.
func wireModel(r media.Request) string {
	if r.WireModel != "" {
		return r.WireModel
	}
	return r.Model
}

// pick returns the requested value when it is in the accepted set, else "".
func pick(want string, allowed []string) string {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, a := range allowed {
		if a == want {
			return want
		}
	}
	return ""
}

// secondsOf reads a duration into its bounds, the fallback when absent or
// unreadable.
func secondsOf(body gjson.Result, fallback int) int {
	v := body.Get("seconds")
	if !v.Exists() {
		v = body.Get("duration")
	}
	if !v.Exists() {
		return fallback
	}
	n := int(v.Int())
	if n < 1 {
		return 1
	}
	if n > 12 {
		return 12
	}
	return n
}

// aspectFor maps a size to an accepted ratio and its dimensions.
func aspectFor(size string) (string, string, string) {
	raw := strings.TrimSpace(size)
	if dims, ok := aspectSizes[raw]; ok {
		return raw, dims[0], dims[1]
	}
	if w, h := media.SizeToDimensions(raw); w > 0 && h > 0 {
		best, bestDelta := "1:1", float64(1<<30)
		for ratio, dims := range aspectSizes {
			dw, dh := media.SizeToDimensions(dims[0] + "x" + dims[1])
			delta := abs(float64(dw)/float64(dh) - float64(w)/float64(h))
			if delta < bestDelta {
				best, bestDelta = ratio, delta
			}
		}
		dims := aspectSizes[best]
		return best, dims[0], dims[1]
	}
	dims := aspectSizes["1:1"]
	return "1:1", dims[0], dims[1]
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// source is the request's input image, when it carries one.
func source(body gjson.Result) string {
	if v := body.Get("image"); v.Exists() {
		return v.String()
	}
	if v := body.Get("image_url"); v.Exists() {
		return v.String()
	}
	if v := body.Get("images.0"); v.Exists() {
		return v.String()
	}
	return ""
}

// setCookie reads one cookie's new value from a response's Set-Cookie headers.
func setCookie(h http.Header, name string) string {
	for _, line := range h.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(line, ";")
		n, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && n == name && v != "" {
			return v
		}
	}
	return ""
}

// claim reads one string claim out of a JWT payload, or "" for a token that is
// not one.
func claim(token, name string) string {
	c, ok := oauth.Claims(token)
	if !ok {
		return ""
	}
	return c.Get(name).String()
}

var _ media.Director = Images{}
var _ media.Director = Videos{}
