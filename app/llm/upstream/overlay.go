package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	appfile "rllmx/app/platform/file"
)

// OverlayFile is the catalog file this program writes into DATA_DIR.
const OverlayFile = "model-catalog.json"

// OverlayVersion stamps the file layout. A file carrying any other version,
// including the reference's own, was built against other tables and is rebuilt.
const OverlayVersion = 3

const (
	maxOverlayBytes     = 8 << 20
	maxOverlayModels    = 1 << 20
	maxOverlayLimit     = 100_000_000
	maxOverlayProviders = 4096
	maxProviderModels   = 100_000
)

// ErrOverlayVersion reports a file laid out by another build.
var ErrOverlayVersion = errors.New("upstream: the catalog file carries another version")

// Modalities are the input kinds models.dev declares for one gateway's model.
type Modalities struct {
	Vision     bool `json:"vision,omitempty"`
	PDF        bool `json:"pdf,omitempty"`
	AudioInput bool `json:"audioInput,omitempty"`
	VideoInput bool `json:"videoInput,omitempty"`
}

// Limits are the gateway's own window and answer cap, where they differ from
// what the tables resolve.
type Limits struct {
	ContextWindow int `json:"contextWindow,omitempty"`
	MaxOutput     int `json:"maxOutput,omitempty"`
}

// Overlay is the synced catalog: deltas only, keyed by local provider id and a
// normalized model id. It is never mutated after it is built.
type Overlay struct {
	Version  int    `json:"v"`
	ETag     string `json:"etag,omitempty"`
	SyncedAt int64  `json:"syncedAt"`
	// Models is keyed "<provider>:<baseId>".
	Models map[string]Modalities `json:"models"`
	// Providers is keyed by provider, then by the registry's model id.
	Providers map[string]map[string]Limits `json:"providers"`
}

// Summary is what the dashboard shows about the installed catalog.
type Summary struct {
	SyncedAt  int64 `json:"syncedAt"`
	Models    int   `json:"models"`
	Providers int   `json:"providers"`
	Bytes     int   `json:"bytes"`
}

// Summary counts what the overlay holds.
func (o *Overlay) Summary() Summary {
	if o == nil {
		return Summary{}
	}
	return Summary{SyncedAt: o.SyncedAt, Models: len(o.Models), Providers: len(o.Providers)}
}

// Modalities answers what a gateway declared for a model.
func (o *Overlay) Modalities(provider, model string) (Modalities, bool) {
	if o == nil || provider == "" {
		return Modalities{}, false
	}
	m, ok := o.Models[provider+":"+baseID(model)]
	return m, ok
}

// Limits answers the delta a gateway's own numbers carry.
func (o *Overlay) Limits(provider, model string) (Limits, bool) {
	if o == nil || provider == "" {
		return Limits{}, false
	}
	byProvider := o.Providers[provider]
	if byProvider == nil {
		return Limits{}, false
	}
	if l, ok := byProvider[model]; ok {
		return l, true
	}
	l, ok := byProvider[baseID(model)]
	return l, ok
}

// baseID normalizes a model id the way models.dev names one: the vendor prefix
// and a ":suffix" tag dropped, lower case.
func baseID(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		model = model[i+1:]
	}
	if i := strings.IndexByte(model, ':'); i >= 0 {
		model = model[:i]
	}
	return strings.ToLower(model)
}

// ParseOverlay reads a catalog file. Only the layout this build writes is
// trusted; anything else is ErrOverlayVersion.
func ParseOverlay(data []byte) (*Overlay, error) {
	if len(data) > maxOverlayBytes {
		return nil, fmt.Errorf("upstream: the catalog file is over %d bytes", maxOverlayBytes)
	}
	var o Overlay
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("upstream: the catalog file: %s", decodeReason(err))
	}
	if dec.More() {
		return nil, errors.New("upstream: the catalog file: more than one JSON value")
	}
	if o.Version != OverlayVersion {
		return nil, ErrOverlayVersion
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	return &o, nil
}

func (o *Overlay) validate() error {
	if len(o.Models) > maxOverlayModels {
		return errors.New("upstream: the catalog lists too many models")
	}
	for key := range o.Models {
		if !strings.Contains(key, ":") || strings.HasPrefix(key, ":") {
			return errors.New("upstream: a catalog model key names no provider")
		}
	}
	if len(o.Providers) > maxOverlayProviders {
		return errors.New("upstream: the catalog lists too many providers")
	}
	for _, models := range o.Providers {
		if len(models) > maxProviderModels {
			return errors.New("upstream: a catalog provider lists too many models")
		}
		for _, l := range models {
			if l.ContextWindow < 0 || l.ContextWindow > maxOverlayLimit || l.MaxOutput < 0 || l.MaxOutput > maxOverlayLimit {
				return errors.New("upstream: a catalog limit is out of range")
			}
			if l.ContextWindow == 0 && l.MaxOutput == 0 {
				return errors.New("upstream: a catalog limit carries no value")
			}
		}
	}
	return nil
}

// ReadOverlayFile reads the catalog file a previous run wrote.
func ReadOverlayFile(path string) (*Overlay, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := appfile.ReadCapped(f, maxOverlayBytes)
	if err != nil {
		return nil, nil, err
	}
	o, err := ParseOverlay(raw)
	return o, raw, err
}

// SetOverlay installs a catalog, or detaches the current one when it is nil.
// The pointer swap is the only synchronization a request pays for.
func (c *Capabilities) SetOverlay(o *Overlay) {
	if o == nil {
		c.overlay.Store(nil)
		return
	}
	c.overlay.Store(o)
}

// Overlay is the catalog in force, for the routes that report it.
func (c *Capabilities) Overlay() *Overlay { return c.overlay.Load() }
