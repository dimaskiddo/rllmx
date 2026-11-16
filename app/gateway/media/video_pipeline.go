package media

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	appfailure "rllmx/app/platform/failure"
	"rllmx/app/store/analytics"
)

// defaultVideoProvider is where a create with no video model goes, as the
// reference sends it.
const defaultVideoProvider = "xai"

// Videos serves POST /v1/videos/{action}: a billable job creation. The client's
// body is forwarded as it arrived; only a JSON body that names another provider
// is rewritten to strip the prefix.
func (p *Pipeline) Videos(ctx context.Context, in Input, action string) Output {
	switch action {
	case media.VideoGenerations, media.VideoEdits, media.VideoExtensions:
	default:
		return p.reject(in, failureOutput(http.StatusBadRequest, "The request names no video action"))
	}
	name, body, out, ok := p.videoTarget(in)
	if !ok {
		return p.reject(in, out)
	}
	return p.run(ctx, upstream.KindVideo, in, parsed{model: name, body: body, action: action})
}

// videoTarget is the routing name and the body to forward.
func (p *Pipeline) videoTarget(in Input) (string, []byte, Output, bool) {
	fallback := func() (string, []byte, Output, bool) {
		name, ok := p.defaultVideo()
		if !ok {
			return "", nil, failureOutput(http.StatusBadRequest, "No video model is configured"), false
		}
		return name, in.Body, Output{}, true
	}
	if ct := strings.ToLower(in.ContentType); ct != "" && !strings.Contains(ct, "json") {
		// A multipart body cannot be read for a model, and re-encoding it would
		// change its boundary.
		return fallback()
	}
	model := strings.TrimSpace(gjson.GetBytes(in.Body, "model").String())
	if model == "" {
		return fallback()
	}
	provider, rest, prefixed := strings.Cut(model, "/")
	if !prefixed || rest == "" {
		// A bare id is not resolved against the chat providers here; the
		// reference sends it to its default video provider.
		return fallback()
	}
	id, resolved := p.d.Registry.Resolve(provider)
	if !resolved || !p.servesVideo(id) {
		return "", nil, failureOutput(http.StatusBadRequest, "That provider does not serve video"), false
	}
	body, err := media.WithModel(in.Body, rest)
	if err != nil {
		return "", nil, failureOutput(http.StatusBadRequest, "The request body cannot carry a model"), false
	}
	return id + "/" + rest, body, Output{}, true
}

// VideoJob serves GET /v1/videos/{id}: a job's status, read from the account
// that created it. A poll never moves to another account and leaves no usage
// row.
func (p *Pipeline) VideoJob(ctx context.Context, in Input, id string) Output {
	if id == "" {
		return failureOutput(http.StatusBadRequest, "The request names no video job")
	}
	provider, ok := p.videoJobProvider(ctx, in)
	if !ok {
		return failureOutput(http.StatusNotFound, "No provider serves that video job")
	}
	entry, found := p.d.Registry.ByID(provider)
	if !found || entry.Media == nil || entry.Media.Video == nil {
		return failureOutput(http.StatusBadRequest, "That provider does not serve video")
	}
	// A poll is a read of a job the client names, and it belongs to the provider
	// it names, so a key needs that provider on its list.
	if !p.mayCall(in.Caller, upstream.MediaRef{Provider: provider, Kind: upstream.KindVideo}) {
		return failureOutput(http.StatusForbidden, appfailure.NotAllowed(provider))
	}
	cfg := entry.Media.Video
	if cfg.ExecutorDriven {
		// A web session renders the video inside the create; there is no job to
		// read back.
		return failureOutput(http.StatusNotFound, "No provider serves that video job")
	}
	adapter, ok := p.d.Adapters.For(provider, upstream.KindVideo, "")
	if !ok {
		return failureOutput(http.StatusBadRequest, "That provider does not serve video here")
	}
	ref := upstream.MediaRef{Provider: provider, Kind: upstream.KindVideo}
	row := analytics.Record{
		Time: p.now(), RequestID: in.RequestID, APIKeyID: in.APIKeyID, APIKeyName: in.KeyName,
		Endpoint: in.Path, Provider: provider,
	}
	conn, out, ok := p.selectAccount(ctx, upstream.KindVideo, entry, ref, "", "", in.ConnectionID, map[string]bool{}, &row, nil)
	if !ok {
		return out
	}
	call := media.Request{Kind: upstream.KindVideo, Provider: provider, Entry: entry, Config: cfg, JobID: id}
	out, done := p.try(ctx, upstream.KindVideo, adapter, entry, cfg, ref, "", conn, call, &row)
	if done {
		return videoPin(out, upstream.KindVideo, conn)
	}
	return out
}

// videoJobProvider is the provider a poll reads: the one the pinned account
// belongs to, then the query's, then the default.
func (p *Pipeline) videoJobProvider(ctx context.Context, in Input) (string, bool) {
	if id := strings.TrimSpace(in.ConnectionID); id != "" && p.d.Connections != nil {
		if conn, ok, err := p.d.Connections.Get(ctx, id); err == nil && ok && p.servesVideo(conn.Provider) {
			return conn.Provider, true
		}
	}
	if in.Provider != "" {
		if id, resolved := p.d.Registry.Resolve(in.Provider); resolved && p.servesVideo(id) {
			return id, true
		}
	}
	if p.servesVideo(defaultVideoProvider) {
		return defaultVideoProvider, true
	}
	return "", false
}

// servesVideo reports whether a provider's data names a video service.
func (p *Pipeline) servesVideo(id string) bool {
	e, ok := p.d.Registry.ByID(id)
	return ok && e.Media != nil && e.Media.Video != nil
}

// defaultVideo is the provider and model a create with no video model goes to:
// the default provider's first video model.
func (p *Pipeline) defaultVideo() (string, bool) {
	entry, ok := p.d.Registry.ByID(defaultVideoProvider)
	if !ok || entry.Media == nil || entry.Media.Video == nil {
		return "", false
	}
	if m := entry.Media.Video.DefaultModel; m != "" {
		return defaultVideoProvider + "/" + m, true
	}
	for _, m := range entry.Models {
		if m.Kind == upstream.KindVideo {
			return defaultVideoProvider + "/" + m.ID, true
		}
	}
	return "", false
}
