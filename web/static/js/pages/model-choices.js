// The model names a picker offers, shared by every page that takes one: the key
// allow-list, a combo's models and the vision and audio adapters.
import { api } from "/static/js/ui/api.js";

// liveModels is every provider/model a request could reach now: the models of a
// provider that has an active connection, spelled the way a client types them.
// A registry list already holds its custom models; a node's come from the custom
// list, under the node's id.
export function liveModels(models, custom, catalog, connections) {
  const live = new Set(connections.filter((c) => c.isActive !== false).map((c) => c.provider));
  const owners = new Set();
  const known = new Set();
  for (const p of catalog) {
    known.add(p.id);
    if (live.has(p.id)) {
      owners.add(p.id);
      if (p.alias) {
        owners.add(p.alias);
      }
    }
  }
  const out = models.filter((m) => owners.has(m.provider)).map((m) => m.fullModel);
  for (const m of custom) {
    if (live.has(m.providerAlias) && !known.has(m.providerAlias)) {
      out.push(m.providerAlias + "/" + m.id);
    }
  }
  return out;
}

// loadModelChoices reads what the pickers need and returns the sorted list: the
// aliases, the combo names and every live model. A list that cannot be read is
// left out, so a picker with fewer names still works.
export async function loadModelChoices() {
  const [aliases, combos, models, custom, catalog, providers] = await Promise.all([
    api("/api/models/alias").catch(() => ({ aliases: {} })),
    api("/api/combos").catch(() => ({ combos: [] })),
    api("/api/models").catch(() => ({ models: [] })),
    api("/api/models/custom").catch(() => ({ models: [] })),
    api("/api/providers/catalog").catch(() => ({ providers: [] })),
    api("/api/providers").catch(() => ({ connections: [] })),
  ]);
  const names = Object.keys(aliases.aliases || {}).concat((combos.combos || []).map((c) => c.name));
  const live = liveModels(models.models || [], custom.models || [], catalog.providers || [], providers.connections || []);
  return Array.from(new Set(names.concat(live))).sort();
}
