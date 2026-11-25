// One provider page: its state, its lifecycle and the small readers the markup
// calls. The work is in the sibling modules, mixed in below.
import { api } from "/static/js/ui/api.js";
import { copy } from "/static/js/ui/copy.js";
import { t } from "/static/js/ui/i18n.js";
import { formatMethods } from "/static/js/ui/format.js";
import { setPath } from "/static/js/ui/fields.js";
import { connectionMethods } from "./provider-connections.js";
import { loginMethods } from "./provider-login.js";
import { modelMethods } from "./provider-models.js";
import { settingsMethods } from "./provider-settings.js";
import { cooldownOf, span } from "./provider-status.js";
import { effectiveStatus, errorTag } from "./providers.js";
import { cardMethods } from "./provider-card.js";
import { fieldMethods } from "/static/js/ui/fields.js";

window.Alpine.data("providerPage", () => Object.assign({
  loading: true,
  error: "",
  notFound: false,
  id: "",
  entry: null,
  node: null,
  providers: [],
  connections: [],
  settings: {},
  strategy: { roundRobin: false, sticky: "3" },
  selected: [],
  now: Date.now(),
  results: null,
  testRun: null,
  timer: null,
  pollTimer: null,
  loginRun: 0,
  gone: false,
  working: {},
  edit: null,
  add: { name: "", key: "", defaultModel: "", priority: "1", region: "", psd: [], check: null, checking: false, busy: false, error: "" },
  bulk: { text: "", results: null, busy: false, error: "" },
  nodeForm: { name: "", prefix: "", apiType: "chat", baseUrl: "", error: "", busy: false },
  models: { items: [], disabled: [], byTarget: {}, test: {}, error: "", loading: false },
  modelAdd: { id: "", name: "", vision: false, reasoning: false, error: "", busy: false },
  aliasForm: { open: false, model: "", alias: "", error: "" },
  overrides: { rows: [], error: "", busy: false },
  login: {
    open: false, kind: "", busy: false, error: "", state: "", authUrl: "", paste: false, input: "",
    userCode: "", verificationUri: "", variant: "", mode: "",
    app: { baseUrl: "", clientId: "", clientSecret: "" },
    token: "", baseUrl: "", email: "", fields: {}, bulk: "", results: null,
  },


  async init() {
    this.id = this.$store.route.params.id || "";
    await this.load();
  },

  destroy() {
    this.gone = true;
    if (this.testRun) {
      this.testRun.on = false;
    }
    this.clearPoll();
    this.login.open = false;
    this.stopTimer();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const [catalog, providers, nodes, settings] = await Promise.all([
        api("/api/providers/catalog"),
        api("/api/providers"),
        api("/api/provider-nodes"),
        api("/api/settings"),
      ]);
      this.providers = catalog.providers || [];
      this.entry = this.providers.find((p) => p.id === this.id) || null;
      this.node = (nodes.nodes || []).find((n) => n.id === this.id) || null;
      this.notFound = !this.entry && !this.node;
      this.connections = (providers.connections || []).filter((c) => c.provider === this.id);
      this.settings = settings;
      this.applyStrategy();
      await Promise.all([this.loadModels(), this.loadOverrides()]);
      if (this.gone) {
        return;
      }
      this.pruneSelection();
      this.startTimer();
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  // The clock only matters while a connection is cooling down, so it runs while
  // some lock is still in the future and the tab is visible. Every status and
  // countdown binding re-evaluates on each tick; with nothing counting down, or
  // nobody looking, that would be wasted work.
  startTimer() {
    this.now = Date.now();
    if (this.gone || this.timer || document.hidden || !this.counting()) {
      return;
    }
    this.timer = setInterval(() => this.tick(), 1000);
  },

  counting() {
    return this.connections.some((conn) => cooldownOf(conn, this.now) > 0);
  },

  tick() {
    this.now = Date.now();
    if (!this.counting()) {
      this.stopTimer();
    }
  },

  stopTimer() {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  },

  // visibilityChanged pauses the clock for a hidden tab and catches it up on
  // return, so a countdown never shows a stale second after the tab comes back.
  visibilityChanged() {
    if (document.hidden) {
      this.stopTimer();
    } else {
      this.startTimer();
    }
  },

  name() {
    return this.node ? this.node.name : (this.entry ? this.entry.name : this.id);
  },

  alias() {
    return this.node ? this.node.prefix : (this.entry ? (this.entry.alias || this.entry.id) : "");
  },

  loginOffer() {
    return this.entry ? this.entry.login : null;
  },

  // controlKeyField is true for the one provider whose quota needs a second
  // credential beside the key the chat path sends.
  controlKeyField() {
    return !this.node && this.id === "opendesign-go";
  },

  canAddKey() {
    if (this.node) {
      return true;
    }
    const category = this.entry ? this.entry.category : "";
    return category === "apikey" || category === "freeTier" || category === "webCookie";
  },

  noPath() {
    return !this.node && !this.canAddKey() && !this.loginOffer();
  },

  title(conn) {
    return conn.name || conn.email || conn.displayName || t("provider.fallback");
  },

  subtitle(conn) {
    const second = conn.email || conn.displayName || "";
    return second && second !== conn.name ? second : "";
  },

  authLabel(conn) {
    const keys = { oauth: "provider.auth.oauth", apikey: "provider.auth.apikey", cookie: "provider.auth.cookie", "mcode-lease": "provider.auth.lease" };
    return keys[conn.authType] ? t(keys[conn.authType]) : (conn.authType || "");
  },

  status(conn) {
    return effectiveStatus(conn, this.now);
  },

  // statusClass colours the row's badge: green when it can be used, amber while
  // it cools down, red when it failed or its login expired.
  statusClass(conn) {
    const status = this.status(conn);
    if (status === "error" || status === "expired") {
      return "badge-err";
    }
    if (status === "unavailable") {
      return "badge-warn";
    }
    return "badge-ok";
  },

  tag(conn) {
    return errorTag(conn);
  },

  cooldown(conn) {
    const until = cooldownOf(conn, this.now);
    return until ? span(until - this.now) : "";
  },

  region(data) {
    return data && typeof data.region === "string" ? data.region : "";
  },

  // busy and run keep one request per control in flight: a second click while
  // the first is out would send a body built from the list as it was.
  busy(key) {
    return this.working[key] === true;
  },

  async run(key, work) {
    if (this.working[key]) {
      return;
    }
    this.working = Object.assign({}, this.working, { [key]: true });
    try {
      await work();
    } finally {
      const next = Object.assign({}, this.working);
      delete next[key];
      this.working = next;
    }
  },

  copyText(text) {
    return copy(text, null);
  },
}, loginMethods, formatMethods, connectionMethods, settingsMethods, modelMethods, fieldMethods, cardMethods));
