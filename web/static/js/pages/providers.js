// The provider list: one card per registry provider and per custom endpoint,
// grouped the way the reference groups them.
import { api } from "/static/js/ui/api.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { cooldownOf } from "./provider-status.js";
import { formatMethods } from "/static/js/ui/format.js";
import { setPath } from "/static/js/ui/fields.js";

// PROVIDER_ICONS points a provider with no image of its own at the mark it
// shares, as the reference does. An id in neither the image directory nor this
// map keeps the initials tile.
const PROVIDER_ICONS = {
  "chatgpt-web": "openai",
  "deepseek-web": "deepseek",
  "gemini-web": "gemini",
  "kiro-social": "kiro",
  "minimax-code": "minimax",
  "minimax-code-global": "minimax",
  "muse-web": "muse",
  "ollama-search": "ollama",
  "opencode-zen": "opencode",
};

// NO_ICON lists the providers with no mark in the reference either; they keep
// the initials tile instead of requesting a file that is not there.
const NO_ICON = ["local-fetch", "runcrate", "synthetic"];

// errorTag names the latest failure of a connection in the few words a card
// shows. The store keeps a status code and a message, nothing richer.
export function errorTag(conn) {
  const code = Number(conn.errorCode);
  if (isFinite(code) && code >= 400) {
    if (code === 401 || code === 403) {
      return "AUTH";
    }
    if (code === 429) {
      return "429";
    }
    if (code >= 500) {
      return "5XX";
    }
    return String(code);
  }
  return conn.lastError ? "ERR" : "";
}

// effectiveStatus treats a connection marked unavailable as active when no
// lock still holds it, the way the reference does.
export function effectiveStatus(conn, now) {
  const status = conn.testStatus || "unknown";
  if (status !== "unavailable") {
    return status;
  }
  return cooldownOf(conn, now) > 0 ? status : "active";
}

// nodeTypeLabel is the words for a custom endpoint's kind.
export function nodeTypeLabel(node) {
  if (!node) {
    return "";
  }
  if (node.type === "anthropic-compatible") {
    return t("providers.nodeTypes.anthropic");
  }
  if (node.type === "custom-embedding") {
    return t("providers.nodeTypes.embedding");
  }
  return node.apiType === "responses" ? t("providers.nodeTypes.responses") : t("providers.nodeTypes.openai");
}

// statsOf counts one provider's connections and names its latest failure.
export function statsOf(connections, now) {
  let connected = 0;
  let failed = 0;
  let latest = null;
  for (const conn of connections) {
    const status = effectiveStatus(conn, now);
    if (status === "active" || status === "success") {
      connected++;
    }
    if (status === "error" || status === "expired" || status === "unavailable") {
      failed++;
    }
    if (conn.lastErrorAt && (!latest || conn.lastErrorAt > latest.lastErrorAt)) {
      latest = conn;
    }
  }
  const allDisabled = connections.length > 0 && connections.every((conn) => conn.isActive === false);
  return {
    total: connections.length,
    connected: connected,
    failed: failed,
    allDisabled: allDisabled,
    tag: latest ? errorTag(latest) : "",
    errorAt: latest ? latest.lastErrorAt : "",
  };
}

// keyCap is how many API-key cards show before "Show all".
const keyCap = 20;

window.Alpine.data("providersPage", () => Object.assign({
  loading: true,
  error: "",
  providers: [],
  connections: [],
  nodes: [],
  search: "",
  filter: "all",
  showAllKey: false,
  view: { nodes: [], oauth: [], free: [], key: [], hiddenKey: 0, any: false },
  testing: "",
  results: null,
  iconsFailed: {},
  add: {
    open: false,
    type: "openai-compatible",
    name: "",
    prefix: "",
    apiType: "chat",
    baseUrl: "",
    apiKey: "",
    modelId: "",
    check: null,
    checking: false,
    busy: false,
    error: "",
  },

  setField(path, event) {
    setPath(this, path, event.target.value);
  },

  // A key or model that changed is no longer the one that was checked.
  setCheckedField(path, event) {
    this.setField(path, event);
    this.add.check = null;
  },

  async init() {
    await this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const [catalog, providers, nodes] = await Promise.all([
        api("/api/providers/catalog"),
        api("/api/providers"),
        api("/api/provider-nodes"),
      ]);
      this.providers = catalog.providers || [];
      this.connections = providers.connections || [];
      this.nodes = nodes.nodes || [];
      this.refreshView();
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  toggleShowAll(key) {
    this[key] = !this[key];
    this.refreshView();
  },

  // setView is setField for the inputs that change what the list shows.
  setView(path, event) {
    this.setField(path, event);
    this.refreshView();
  },

  nodeLabel(card) {
    return nodeTypeLabel(card.entry.node);
  },

  // errorBadge is one badge, "2 Error (429)", rather than the count, the word
  // and the tag written side by side. A last error with no status code adds no
  // parenthetical.
  errorBadge(card) {
    if (!card.failed) {
      return "";
    }
    const tag = card.tag && card.tag !== "ERR" ? " (" + card.tag + ")" : "";
    return card.failed + " " + t("providers.error") + tag;
  },

  matches(card) {
    if (this.search) {
      const word = this.search.toLowerCase();
      if (card.entry.name.toLowerCase().indexOf(word) < 0 && card.entry.id.toLowerCase().indexOf(word) < 0) {
        return false;
      }
    }
    if (this.filter === "active") {
      return (card.entry.login !== undefined && card.total === 0) || (card.total > 0 && !card.allDisabled);
    }
    if (this.filter === "inactive") {
      return card.allDisabled;
    }
    if (this.filter === "none") {
      return card.total === 0;
    }
    return true;
  },

  byConnected(a, b) {
    const ac = a.total > 0 ? 0 : 1;
    const bc = b.total > 0 ? 0 : 1;
    return ac !== bc ? ac - bc : a.entry.name.localeCompare(b.entry.name);
  },

  // refreshView builds every section once. The markup reads the result, so a
  // keystroke in the search box or a reload does the work one time, not once per
  // binding. The connections are indexed by provider first, which keeps the cost
  // linear in providers plus connections.
  refreshView() {
    const now = Date.now();
    const byProvider = new Map();
    for (const conn of this.connections) {
      const list = byProvider.get(conn.provider);
      if (list) {
        list.push(conn);
      } else {
        byProvider.set(conn.provider, [conn]);
      }
    }
    const cardFor = (entry) => {
      const conns = byProvider.get(entry.id) || [];
      return Object.assign({ entry: entry, connectionIds: conns.map((c) => c.id) }, statsOf(conns, now));
    };
    const section = (categories) => this.providers
      .filter((p) => categories.indexOf(p.category) >= 0 && p.kinds.indexOf("llm") >= 0)
      .map(cardFor)
      .filter((card) => this.matches(card))
      .sort((a, b) => this.byConnected(a, b));
    const order = ["openai-compatible", "anthropic-compatible", "custom-embedding"];
    const nodes = this.nodes
      .map((node) => cardFor({ id: node.id, name: node.name, alias: node.prefix, login: undefined, node: node }))
      .filter((card) => this.matches(card))
      .sort((a, b) => order.indexOf(a.entry.node.type) - order.indexOf(b.entry.node.type));
    const oauth = section(["oauth"]);
    const free = section(["free", "freeTier"]);
    const keyed = section(["apikey", "webCookie"]);
    const capped = !this.showAllKey && !this.search && this.filter === "all" && keyed.length > keyCap;
    const key = capped ? keyed.slice(0, keyCap) : keyed;
    this.view = {
      nodes: nodes, oauth: oauth, free: free, key: key,
      hiddenKey: capped ? keyed.length - keyCap : 0,
      any: nodes.length + oauth.length + free.length + key.length > 0,
    };
  },

  statusKey(card) {
    if (card.allDisabled) {
      return "providers.disabled";
    }
    if (card.entry.login !== undefined && card.total === 0) {
      return "providers.ready";
    }
    if (card.total === 0) {
      return "providers.noneYet";
    }
    return "";
  },

  statusText(card) {
    const key = this.statusKey(card);
    return key ? t(key) : "";
  },

  async toggleProvider(card) {
    const next = card.allDisabled;
    let failed = 0;
    await Promise.all(card.connectionIds.map(async (id) => {
      try {
        await api("/api/providers/" + encodeURIComponent(id), { method: "PUT", body: { isActive: next } });
      } catch (err) {
        failed++;
      }
    }));
    if (failed) {
      toast().error(t("providers.toggleFailed"));
    }
    await this.load();
  },

  async testGroup(mode) {
    if (this.testing) {
      return;
    }
    this.testing = mode;
    try {
      const out = await api("/api/providers/test-batch", { method: "POST", body: { mode: mode } });
      this.results = out;
      this.showResults();
    } catch (err) {
      toast().error(err.message);
    } finally {
      this.testing = "";
      await this.load();
    }
  },

  showResults() {
    this.$refs.resultsDlg.showModal();
  },

  closeResults() {
    this.$refs.resultsDlg.close();
    this.results = null;
  },

  openAdd(type) {
    this.add = {
      open: true, type: type, name: "", prefix: "", apiType: "chat", baseUrl: "",
      apiKey: "", modelId: "", check: null, checking: false, busy: false, error: "",
    };
    this.$refs.addDlg.showModal();
    this.$refs.addName.focus();
  },

  forgetAdd() {
    this.add.apiKey = "";
    this.add.check = null;
  },

  closeAdd() {
    this.$refs.addDlg.close();
    this.add.open = false;
  },

  async checkNode() {
    this.add.checking = true;
    this.add.check = null;
    this.add.error = "";
    try {
      this.add.check = await api("/api/provider-nodes/validate", {
        method: "POST",
        body: {
          baseUrl: this.add.baseUrl,
          apiKey: this.add.apiKey,
          type: this.add.type,
          modelId: this.add.modelId,
        },
      });
    } catch (err) {
      this.add.error = err.message;
    } finally {
      this.add.checking = false;
    }
  },

  async saveNode() {
    this.add.error = "";
    this.add.busy = true;
    const body = { name: this.add.name, prefix: this.add.prefix, type: this.add.type, baseUrl: this.add.baseUrl };
    if (this.add.type === "openai-compatible") {
      body.apiType = this.add.apiType;
    }
    try {
      await api("/api/provider-nodes", { method: "POST", body: body });
      this.closeAdd();
      await this.load();
      toast().success(t("providers.nodeSaved"));
    } catch (err) {
      this.add.error = err.message;
    } finally {
      this.add.busy = false;
    }
  },

  initials(card) {
    const name = card.entry.name || card.entry.id;
    return name.slice(0, 2).toUpperCase();
  },

  // iconSrc is the logo a card shows; empty means the initials tile, and a
  // failed load falls back to it too.
  iconSrc(card) {
    if (card.entry.node) {
      if (card.entry.node.type === "anthropic-compatible") {
        return "/static/img/providers/anthropic-m.png";
      }
      return card.entry.node.apiType === "responses" ? "/static/img/providers/oai-r.png" : "/static/img/providers/oai-cc.png";
    }
    const id = card.entry.id || "";
    if (NO_ICON.indexOf(id) >= 0) {
      return "";
    }
    return "/static/img/providers/" + (PROVIDER_ICONS[id] || id) + ".png";
  },

  failIcon(card) {
    this.iconsFailed = Object.assign({}, this.iconsFailed, { [card.entry.id]: true });
  },

  detailHref(id) {
    return "/dashboard/providers/" + encodeURIComponent(id);
  },
}, formatMethods));
