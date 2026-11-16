package upstream

import "strings"

// MediaRef is one media model of one provider: where a media request goes.
type MediaRef struct {
	Provider string
	Kind     string
	Model    string
}

// indexMedia records a provider's media models for lookup. The same id may name
// models of different kinds; the kind a request asks for picks one.
func (r *Registry) indexMedia(e Entry) {
	add := func(kind, id string) {
		if id == "" {
			return
		}
		ref := MediaRef{Provider: e.ID, Kind: kind, Model: id}
		r.mediaOrder = append(r.mediaOrder, ref)
		r.byKind[kind] = append(r.byKind[kind], ref)
		r.mediaKey[e.ID+"/"+id+"/"+kind] = ref
		for _, prev := range r.mediaBare[id] {
			if prev == ref {
				return
			}
		}
		r.mediaBare[id] = append(r.mediaBare[id], ref)
	}
	for _, m := range e.Models {
		if m.Kind != "" && m.Kind != KindLLM {
			add(m.Kind, m.ID)
		}
	}
	for _, kind := range []string{KindEmbedding, KindImage, KindTTS, KindSTT, KindVideo, KindSearch, KindFetch} {
		cfg := e.Media.Config(kind)
		if cfg == nil {
			continue
		}
		for _, m := range cfg.Models {
			add(kind, m.ID)
		}
	}
}

// Media resolves a media model name for one service. A model id may itself hold
// a slash (a HuggingFace or Cloudflare id), so an exact id wins before a
// provider prefix is read; a bare id must name exactly one model of that kind.
func (r *Registry) Media(name, kind string) (MediaRef, bool) {
	var match []MediaRef
	for _, ref := range r.mediaBare[name] {
		if kind == "" || ref.Kind == kind {
			match = append(match, ref)
		}
	}
	if len(match) == 1 {
		return match[0], true
	}
	provider, model, ok := strings.Cut(name, "/")
	if !ok {
		return MediaRef{}, false
	}
	id, resolved := r.Resolve(provider)
	if !resolved {
		return MediaRef{}, false
	}
	if kind != "" {
		ref, ok := r.mediaKey[id+"/"+model+"/"+kind]
		return ref, ok
	}
	for _, k := range mediaKindList {
		if ref, ok := r.mediaKey[id+"/"+model+"/"+k]; ok {
			return ref, true
		}
	}
	return MediaRef{}, false
}

// mediaKindList is every service kind, for a lookup that does not name one.
var mediaKindList = []string{KindEmbedding, KindImage, KindTTS, KindSTT, KindVideo, KindSearch, KindFetch}

// Allows reports whether a key's list names the resolved service. A model route
// is named as provider/model, or as the bare model id the media index knows; a
// provider-only route (a search, a fetch, a video poll, a voice list) is named
// by a bare entry that resolves to that provider. An entry with a slash never
// opens a provider-only route, and a bare provider never opens a model: the two
// are different things to be granted.
func (r *Registry) Allows(list []string, ref MediaRef) bool {
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		provider, model, hasModel := strings.Cut(entry, "/")
		if ref.Model == "" {
			if hasModel {
				continue
			}
			if id, ok := r.Resolve(entry); ok && id == ref.Provider {
				return true
			}
			continue
		}
		if hasModel && model != "" {
			if id, ok := r.Resolve(provider); ok && id == ref.Provider && model == ref.Model {
				return true
			}
			continue
		}
		if m, ok := r.Media(entry, ref.Kind); ok && m.Provider == ref.Provider && m.Model == ref.Model {
			return true
		}
	}
	return false
}
