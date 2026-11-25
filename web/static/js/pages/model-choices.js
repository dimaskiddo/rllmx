// The model names a picker offers, shared by every page that takes one: the key
// allow-list, a combo's models and the vision and audio adapters.
import { api } from "/static/js/ui/api.js";

// The list is read once per page and kept until something that feeds it changes:
// a picker that refetches on every load re-diffs its whole datalist.
let loaded = null;
let loading = null;

// nodePrefix maps a node's id to the prefix a client types in front of its
// models. The connection carries a copy of the node's prefix.
function nodePrefix(connections) {
  const out = new Map();
  for (const c of connections) {
    const prefix = c.providerSpecificData && c.providerSpecificData.prefix;
    if (prefix) {
      out.set(c.provider, prefix);
    }
  }
  return out;
}

// liveModels is every provider/model a request could reach now: the models of a
// provider that has an active connection, spelled the way a client types them.
// A registry list already holds its custom models; a node's come from the custom
// list, under the node's prefix.
export function liveModels(models, custom, catalog, connections) {
  const prefixOf = nodePrefix(connections);
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
      out.push((prefixOf.get(m.providerAlias) || m.providerAlias) + "/" + m.id);
    }
  }
  return out;
}

// normalizeModelID rewrites a stored name that starts with a node's id to the
// node's prefix, so a combo or adapter saved before the picker changed is
// rewritten when its form is next saved. Anything else is left alone.
export function normalizeModelID(value) {
  return loaded ? withNodePrefix(value, loaded.prefixOf) : value;
}

// invalidateModelChoices drops the cached list after a change that adds or
// removes a name.
export function invalidateModelChoices() {
  loaded = null;
}

// withNodePrefix rewrites a stored name that starts with a node's id to the
// node's prefix, the spelling a client types.
function withNodePrefix(value, prefixOf) {
  const at = value.indexOf("/");
  if (at <= 0) {
    return value;
  }
  const prefix = prefixOf.get(value.slice(0, at));
  return prefix ? prefix + value.slice(at) : value;
}

// loadModelChoices reads what the pickers need and returns the sorted list: the
// combo names, every alias's target and every live model. An alias key is left
// out: it names no provider, so offered alone it reads as a model of its own.
// A list that cannot be read is left out, so a picker with fewer names still
// works. known carries answers the caller already has, so the two heavy lists
// are not read twice.
export async function loadModelChoices(known) {
  const have = known || {};
  if (loaded) {
    return loaded.choices;
  }
  if (!loading) {
    loading = (async () => {
      const [aliases, combos, models, custom, catalog, providers] = await Promise.all([
        api("/api/models/alias").catch(() => ({ aliases: {} })),
        have.combos ? Promise.resolve({ combos: have.combos }) : api("/api/combos").catch(() => ({ combos: [] })),
        have.models ? Promise.resolve({ models: have.models }) : api("/api/models").catch(() => ({ models: [] })),
        api("/api/models/custom").catch(() => ({ models: [] })),
        api("/api/providers/catalog").catch(() => ({ providers: [] })),
        api("/api/providers").catch(() => ({ connections: [] })),
      ]);
      const prefixOf = nodePrefix(providers.connections || []);
      const names = (combos.combos || []).map((c) => c.name);
      for (const target of Object.values(aliases.aliases || {})) {
        if (typeof target === "string" && target.indexOf("/") > 0) {
          names.push(withNodePrefix(target, prefixOf));
        }
      }
      const live = liveModels(models.models || [], custom.models || [], catalog.providers || [], providers.connections || []);
      // A stored name is trimmed where it is suggested, so a padded one cannot
      // show up padded even though it still resolves as stored.
      return {
        choices: Array.from(new Set(names.concat(live).map((c) => c.trim()).filter(Boolean))).sort(),
        prefixOf: prefixOf,
      };
    })();
  }
  try {
    loaded = await loading;
  } finally {
    loading = null;
  }
  return loaded.choices;
}
