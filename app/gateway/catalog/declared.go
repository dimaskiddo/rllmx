package catalog

import (
	"context"
	"encoding/json"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// LoadDeclared reads the capability flags an operator stored for custom models
// into the resolver. The models page refreshes them after a write; a runner
// calls this once when the stores open, so a stored flag decides routing and
// ordering before the page is ever opened. A row that cannot be read is skipped;
// an unreadable scope is the caller's error to log.
func LoadDeclared(ctx context.Context, kv KV, caps *upstream.Capabilities) error {
	if caps == nil {
		return nil
	}
	raw, err := kv.List(ctx, repository.ScopeCustomModels)
	if err != nil {
		return err
	}
	all := make(map[string]upstream.Declared, len(raw))
	for _, body := range raw {
		var stored struct {
			ProviderAlias string          `json:"providerAlias"`
			ID            string          `json:"id"`
			Caps          map[string]bool `json:"caps"`
		}
		if json.Unmarshal([]byte(body), &stored) != nil || stored.ProviderAlias == "" || stored.ID == "" {
			continue
		}
		var d upstream.Declared
		if v, ok := stored.Caps["vision"]; ok {
			d.Vision = &v
		}
		if v, ok := stored.Caps["reasoning"]; ok {
			d.Reasoning = &v
		}
		if v, ok := stored.Caps["audio"]; ok {
			d.Audio = &v
		}
		all[stored.ProviderAlias+"/"+stored.ID] = d
	}
	caps.SetCustom(all)
	return nil
}
