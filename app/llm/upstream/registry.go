package upstream

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	appfile "rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

//go:embed data/*.json
var embedded embed.FS

const (
	maxFileBytes = 1 << 20
	maxNameBytes = 256
)

// ErrNoProviders means the data directory held no provider files.
var ErrNoProviders = errors.New("upstream: no provider data files found")

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Registry is the validated set of providers. It is read-only after Load.
type Registry struct {
	entries map[string]Entry
	tokens  map[string]string
	order   []string

	mediaOrder []MediaRef
	mediaBare  map[string][]MediaRef
	mediaKey   map[string]MediaRef
	byKind     map[string][]MediaRef
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
	defaultErr  error
)

// Default returns the registry built from the embedded data, once.
func Default() (*Registry, error) {
	defaultOnce.Do(func() { defaultReg, defaultErr = Load(embedded, "data") })
	return defaultReg, defaultErr
}

// Load reads every <id>.json in dir and validates the set as a whole. Errors
// name files and fields and never repeat file content.
func Load(fsys fs.FS, dir string) (*Registry, error) {
	names, err := fs.Glob(fsys, path.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("upstream: list %s: %w", dir, err)
	}
	if len(names) == 0 {
		return nil, ErrNoProviders
	}
	r := &Registry{
		entries: map[string]Entry{}, tokens: map[string]string{},
		mediaBare: map[string][]MediaRef{}, mediaKey: map[string]MediaRef{},
		byKind: map[string][]MediaRef{},
	}
	var errs []error
	for _, name := range names {
		e, err := readEntry(fsys, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if want := strings.TrimSuffix(path.Base(name), ".json"); e.ID != want {
			errs = append(errs, fmt.Errorf("upstream: %s: file name must equal the provider id", path.Base(name)))
			continue
		}
		if err := r.add(e); err != nil {
			errs = append(errs, fmt.Errorf("upstream: %s: %w", path.Base(name), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, id := range r.order {
		fallback := r.entries[id].Media
		if fallback == nil || fallback.CredentialFallback == "" {
			continue
		}
		if target, ok := r.Resolve(fallback.CredentialFallback); !ok || target == id {
			errs = append(errs, fmt.Errorf("upstream: %s.json: credentialFallback names no other provider", id))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.Sort(r.order)
	for kind := range r.byKind {
		slices.SortStableFunc(r.byKind[kind], func(a, b MediaRef) int {
			return strings.Compare(a.Provider+"/"+a.Model, b.Provider+"/"+b.Model)
		})
	}
	return r, nil
}

func readEntry(fsys fs.FS, name string) (Entry, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return Entry{}, fmt.Errorf("upstream: open %s: %w", path.Base(name), err)
	}
	defer f.Close()
	raw, err := appfile.ReadCapped(f, maxFileBytes)
	if errors.Is(err, appfile.ErrTooLarge) {
		return Entry{}, fmt.Errorf("upstream: %s: file is over %d bytes", path.Base(name), maxFileBytes)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("upstream: read %s: %w", path.Base(name), err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Entry
	if err := dec.Decode(&e); err != nil {
		return Entry{}, fmt.Errorf("upstream: %s: %s", path.Base(name), decodeReason(err))
	}
	if dec.More() {
		return Entry{}, fmt.Errorf("upstream: %s: more than one JSON value", path.Base(name))
	}
	return e, nil
}

func (r *Registry) add(e Entry) error {
	if !idPattern.MatchString(e.ID) {
		return errors.New("id must be lower-case letters, digits and dashes")
	}
	if _, dup := r.entries[e.ID]; dup {
		return errors.New("duplicate id")
	}
	if !categories[e.Category] {
		return errors.New("unknown category")
	}
	if e.NoAuth && (e.Auth != nil || e.OAuth != nil) {
		return errors.New("a no-auth provider carries a credential block")
	}
	if e.Transport.Format == "" {
		e.Transport.Format = FormatOpenAI
	}
	if !formats[e.Transport.Format] {
		return errors.New("unknown transport format")
	}
	if e.Executor == "" {
		e.Executor = ExecutorDefault
	}
	if !executors[e.Executor] {
		return errors.New("unknown executor")
	}
	if e.Display.Name == "" {
		e.Display.Name = e.ID
	}
	authHeader := ""
	if e.Auth != nil {
		authHeader = e.Auth.Header
		if err := e.Auth.validate(e.Transport.BaseURL); err != nil {
			return err
		}
	}
	mediaOnly := e.Transport.BaseURL == "" && e.Media != nil
	if err := e.Transport.validate(e.Executor, authHeader, mediaOnly); err != nil {
		return err
	}
	if e.OAuth != nil {
		if err := e.OAuth.validate(); err != nil {
			return err
		}
	}
	chatModels, err := validateModels(e.Models)
	if err != nil {
		return err
	}
	if err := e.Media.validate(chatModels); err != nil {
		return err
	}
	for _, tok := range append([]string{e.ID, e.Alias}, e.Aliases...) {
		if tok == "" {
			continue
		}
		if !idPattern.MatchString(tok) {
			return errors.New("an alias must be lower-case letters, digits and dashes")
		}
		if owner, taken := r.tokens[tok]; taken && owner != e.ID {
			return errors.New("an alias is already used by another provider")
		}
		if local, ok := localNames[tok]; ok && local != e.ID {
			return errors.New("a name is reserved for a local provider")
		}
		r.tokens[tok] = e.ID
	}
	r.entries[e.ID] = e
	r.order = append(r.order, e.ID)
	r.indexMedia(e)
	return nil
}

// validateModels checks one provider's model rows and returns the ids of its
// chat models, which a media block may refer to.
func validateModels(models []Model) (map[string]bool, error) {
	seenKind := map[string]string{}
	chatModels := map[string]bool{}
	for _, m := range models {
		if m.ID == "" || len(m.ID) > maxNameBytes {
			return nil, errors.New("a model id is empty or over 256 bytes")
		}
		if m.Kind != "" && !mediaKinds[m.Kind] {
			return nil, errors.New("a model has an unknown kind")
		}
		if m.Kind == "" || m.Kind == KindLLM {
			chatModels[m.ID] = true
		}
		if m.Dimensions < 0 || m.Dimensions > 1<<20 || len(m.Params) > 32 || len(m.Transport) > 64 || text.HasASCIIControl(m.Transport) {
			return nil, errors.New("a model has a bad media field")
		}
		if m.ContextLength < 0 || m.ContextLength > maxOverlayLimit {
			return nil, errors.New("a model has a bad context length")
		}
		if err := validateVoices(m.Voices); err != nil {
			return nil, err
		}
		for _, p := range m.Params {
			if p == "" || len(p) > 64 || text.HasASCIIControl(p) {
				return nil, errors.New("a model has a bad parameter name")
			}
		}
		// One id may name a chat model and a media model (a provider that serves
		// both under the same name); two models of one kind may not share it.
		if prev, dup := seenKind[m.ID]; dup && prev == m.Kind {
			return nil, errors.New("duplicate model id")
		}
		seenKind[m.ID] = m.Kind
		if m.TargetFormat != "" && !formats[m.TargetFormat] {
			return nil, errors.New("a model has an unknown target format")
		}
		if len(m.UpstreamID) > maxNameBytes || text.HasASCIIControl(m.UpstreamID) {
			return nil, errors.New("a model has a bad upstream id")
		}
		if len(m.Levels) > len(effortLevels) {
			return nil, errors.New("a model has too many levels")
		}
		for _, l := range m.Levels {
			if !effortLevels[l] {
				return nil, errors.New("a model has an unknown level")
			}
		}
	}
	return chatModels, nil
}

// ByID returns the provider with that canonical id.
func (r *Registry) ByID(id string) (Entry, bool) {
	e, ok := r.entries[id]
	return e, ok
}

// Resolve turns an id, alias, extra alias or local name into the provider id.
// An unknown name is not resolved.
func (r *Registry) Resolve(token string) (string, bool) {
	if id, ok := r.tokens[token]; ok {
		return id, true
	}
	id, ok := localNames[token]
	return id, ok
}

// Models returns the provider's model list; nil for an unknown provider.
func (r *Registry) Models(id string) []Model {
	e, ok := r.entries[id]
	if !ok {
		return nil
	}
	return slices.Clone(e.Models)
}

// Model returns the registry row of a model, if the provider lists it.
func (r *Registry) Model(id, model string) (Model, bool) {
	for _, m := range r.entries[id].Models {
		if m.ID == model {
			return m, true
		}
	}
	return Model{}, false
}

// WireModel is the id to send to the provider for a model: the listed upstream
// id when there is one, else the model as it came.
func (r *Registry) WireModel(id, model string) string {
	for _, m := range r.entries[id].Models {
		if m.ID == model && m.UpstreamID != "" {
			return m.UpstreamID
		}
	}
	return model
}

// All returns every provider ordered by id.
func (r *Registry) All() []Entry {
	out := make([]Entry, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.entries[id])
	}
	return out
}

// Reserved reports whether a name belongs to a built-in provider: an id, an
// alias or a local name. A node prefix may never take such a name over.
func (r *Registry) Reserved(name string) bool {
	_, ok := r.Resolve(name)
	return ok
}

// decodeReason says why a data file did not decode without repeating what the
// decoder echoes: an unknown key or a bad value can be text pasted by mistake.
func decodeReason(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "not valid JSON"
	case errors.As(err, &typ):
		return "field " + typ.Field + " has the wrong type"
	case strings.Contains(err.Error(), "unknown field"):
		return "unknown field"
	default:
		return "cannot be decoded"
	}
}
