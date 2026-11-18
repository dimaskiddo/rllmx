package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	appfile "rllmx/app/platform/file"
)

// ModelsDevURL is the catalog this build syncs. The data is MIT.
const ModelsDevURL = "https://models.dev/api.json"

// MaxCatalogBytes bounds the catalog download. The published file is about
// 4.3 MB; the rest is headroom for a larger one.
const MaxCatalogBytes = 32 << 20

const (
	maxCatalogProviders = 2000
	maxCatalogModels    = 20_000
	// limitTolerance ignores a difference a gateway rounds away, such as
	// 200000 against 202752.
	limitTolerance = 0.1
)

// ErrSyncRunning reports a sync already in flight.
var ErrSyncRunning = errors.New("upstream: a catalog sync is already running")

// overlayAliases maps a local provider id to the models.dev one for the
// gateways models.dev files under another name.
var overlayAliases = map[string]string{
	"github":        "github-copilot",
	"glm":           "zai",
	"glm-cn":        "zhipuai",
	"claude":        "anthropic",
	"gemini":        "google",
	"kimi":          "moonshotai",
	"cloudflare-ai": "cloudflare-workers-ai",
}

// SyncResult is the outcome of one sync: "updated" or "unchanged".
type SyncResult struct {
	Status    string `json:"status"`
	ETag      string `json:"etag,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`
	Models    int    `json:"models,omitempty"`
	Providers int    `json:"providers,omitempty"`
}

// SyncState is everything the dashboard's status route reports.
type SyncState struct {
	Running     bool        `json:"running"`
	LastSync    int64       `json:"lastSync"`
	LastError   string      `json:"lastError,omitempty"`
	LastResult  *SyncResult `json:"lastResult,omitempty"`
	ETag        string      `json:"etag,omitempty"`
	FileVersion int         `json:"fileVersion"`
	File        string      `json:"file"`
	URL         string      `json:"url"`
	IntervalMs  int64       `json:"intervalMs"`
	Catalog     *Summary    `json:"catalog"`
}

// devProvider is the part of a models.dev provider this build reads.
type devProvider struct {
	Models map[string]devModel `json:"models"`
}

type devModel struct {
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context float64 `json:"context"`
		Output  float64 `json:"output"`
	} `json:"limit"`
}

// DecodeCatalog reads models.dev's catalog one provider at a time, under a byte
// cap the caller enforces and the count caps here.
func DecodeCatalog(r io.Reader) (map[string]devProvider, error) {
	dec := json.NewDecoder(r)
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("upstream: the catalog is not a JSON object")
	}
	out := make(map[string]devProvider, 256)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("upstream: the catalog is not a JSON object")
		}
		key, _ := tok.(string)
		if key == "" {
			return nil, errors.New("upstream: the catalog names a provider with no id")
		}
		if len(out) >= maxCatalogProviders {
			return nil, fmt.Errorf("upstream: the catalog lists over %d providers", maxCatalogProviders)
		}
		var p devProvider
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("upstream: a catalog provider: %s", decodeReason(err))
		}
		if len(p.Models) > maxCatalogModels {
			return nil, fmt.Errorf("upstream: a catalog provider lists over %d models", maxCatalogModels)
		}
		out[key] = p
	}
	if len(out) == 0 {
		return nil, errors.New("upstream: the catalog lists no provider")
	}
	return out, nil
}

// FetchOverlay downloads the catalog and builds the deltas against this build's
// tables. A 304 means the caller's file is still current, and then neither the
// overlay nor its bytes are returned.
func (c *Capabilities) FetchOverlay(ctx context.Context, client *http.Client, etag string) (*Overlay, []byte, SyncResult, error) {
	if client == nil {
		return nil, nil, SyncResult{}, errors.New("upstream: no client for the catalog")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsDevURL, nil)
	if err != nil {
		return nil, nil, SyncResult{}, errors.New("upstream: the catalog request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, SyncResult{}, errors.New("upstream: the catalog did not answer in time")
		}
		return nil, nil, SyncResult{}, errors.New("upstream: the catalog could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, nil, SyncResult{Status: "unchanged", ETag: etag}, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, nil, SyncResult{}, fmt.Errorf("upstream: the catalog answered %d", resp.StatusCode)
	}
	raw, err := appfile.ReadCapped(resp.Body, MaxCatalogBytes)
	if err != nil {
		return nil, nil, SyncResult{}, errors.New("upstream: the catalog is too large")
	}
	cat, err := DecodeCatalog(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, SyncResult{}, err
	}
	out := c.BuildOverlay(cat)
	out.ETag = resp.Header.Get("ETag")
	out.SyncedAt = time.Now().UnixMilli()
	data, err := json.Marshal(out)
	if err != nil {
		return nil, nil, SyncResult{}, errors.New("upstream: the catalog could not be written")
	}
	return out, data, SyncResult{
		Status: "updated", ETag: out.ETag, Bytes: len(data),
		Models: len(out.Models), Providers: len(out.Providers),
	}, nil
}

// BuildOverlay turns the upstream catalog into the deltas this build needs:
// declared modalities per gateway, and the limits that differ from the tables.
func (c *Capabilities) BuildOverlay(cat map[string]devProvider) *Overlay {
	out := &Overlay{
		Version:   OverlayVersion,
		Models:    map[string]Modalities{},
		Providers: map[string]map[string]Limits{},
	}
	owners := c.localIDs()
	// A gateway is filed under every local id requests arrive with, and under
	// the upstream id too, because a provider node may carry that name.
	for _, upstream := range sortedKeys(cat) {
		locals := owners[upstream]
		if len(locals) == 0 {
			locals = []string{upstream}
		}
		byBase := map[string]devModel{}
		seen := map[string]bool{}
		for _, modelID := range sortedKeys(cat[upstream].Models) {
			id := baseID(modelID)
			m := cat[upstream].Models[modelID]
			if _, ok := byBase[id]; !ok {
				byBase[id] = m
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			declared := modalitiesOf(m)
			if declared == (Modalities{}) {
				continue
			}
			for _, local := range locals {
				out.Models[local+":"+id] = declared
			}
			if !slices.Contains(locals, upstream) {
				out.Models[upstream+":"+id] = declared
			}
		}
		if c.registry == nil {
			continue
		}
		for _, local := range locals {
			entries, ok := c.registry.ByID(local)
			if !ok {
				continue
			}
			for _, model := range entries.Models {
				if model.Kind != "" && model.Kind != "llm" {
					continue
				}
				dev, ok := byBase[baseID(model.ID)]
				if !ok {
					continue
				}
				cur := c.Base(local, model.ID)
				delta := Limits{}
				if ctx := toInt(dev.Limit.Context); ctx > 0 && model.ContextLength == 0 &&
					differs(ctx, cur.ContextWindow) {
					delta.ContextWindow = ctx
				}
				if o := toInt(dev.Limit.Output); o > 0 && differs(o, cur.MaxOutput) {
					delta.MaxOutput = o
				}
				if delta == (Limits{}) {
					continue
				}
				if out.Providers[local] == nil {
					out.Providers[local] = map[string]Limits{}
				}
				out.Providers[local][model.ID] = delta
			}
		}
	}
	return out
}

// localIDs maps a models.dev provider id to the local ids that request it.
func (c *Capabilities) localIDs() map[string][]string {
	out := map[string][]string{}
	if c.registry == nil {
		return out
	}
	for _, e := range c.registry.All() {
		upstream := e.ID
		if a, ok := overlayAliases[e.ID]; ok {
			upstream = a
		}
		if !slices.Contains(out[upstream], e.ID) {
			out[upstream] = append(out[upstream], e.ID)
		}
	}
	return out
}

// differs reports a value the tables do not already carry, beyond the tolerance.
func differs(v, cur int) bool {
	if cur <= 0 {
		return true
	}
	return math.Abs(float64(v-cur))/float64(cur) > limitTolerance
}

func toInt(f float64) int {
	if f <= 0 || f > maxOverlayLimit {
		return 0
	}
	return int(f)
}

func modalitiesOf(m devModel) Modalities {
	var out Modalities
	for _, in := range m.Modalities.Input {
		switch in {
		case "image":
			out.Vision = true
		case "pdf":
			out.PDF = true
		case "audio":
			out.AudioInput = true
		case "video":
			out.VideoInput = true
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
