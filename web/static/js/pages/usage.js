// Usage: the period totals, the live feed, the charts and the request log.
import { api } from "/static/js/ui/api.js";
import { session } from "/static/js/ui/session.js";
import { t } from "/static/js/ui/i18n.js";
import { usageChartMethods } from "./usage-charts.js";
import { formatMethods } from "/static/js/ui/format.js";

const periods = ["today", "24h", "7d", "30d", "60d", "all"];

// Every stored failure class gets a word: the reader acts on the reason, not
// the code. A class this map does not know keeps its code.
const failureWords = {
  client_closed: "usage.class.client_closed",
  timeout: "usage.class.timeout",
  stream_stall: "usage.class.stream_stall",
  truncated: "usage.class.truncated",
  network: "usage.class.network",
  upstream_5xx: "usage.class.upstream_5xx",
  rate_limit: "usage.class.rate_limit",
  key_quota: "usage.class.key_quota",
  quota: "usage.class.quota",
  auth: "usage.class.auth",
  access_denied: "usage.class.access_denied",
  expired: "usage.class.expired",
  no_account: "usage.class.no_account",
  capacity: "usage.class.capacity",
  invalid_request: "usage.class.invalid_request",
  not_found: "usage.class.not_found",
  unknown: "usage.class.unknown",
};

function emptyStats() {
  return {
    totalRequests: 0, totalPromptTokens: 0, totalCompletionTokens: 0, totalCachedTokens: 0, totalCost: 0,
    byProvider: {}, byModel: {}, byAccount: {}, byApiKey: {}, byEndpoint: {},
    pending: { byModel: {}, byAccount: {} }, activeRequests: [], recentRequests: [], errorProvider: "",
  };
}

// groupBy folds one flat server entry into its summary row. The server keys
// an entry per model and provider, so a key, account, model or endpoint arrives
// several times and the summary is their sum.
const groupBy = {
  byModel: {
    key: (e) => e.rawModel,
    name: (e) => e.rawModel,
    detail: () => "",
    itemName: (e) => e.provider,
    itemDetail: (e) => e.rawModel,
  },
  byAccount: {
    key: (e) => e.connectionId || e.accountName,
    name: (e) => e.accountName,
    detail: (e) => e.provider,
    itemName: (e) => e.provider,
    itemDetail: (e) => e.rawModel,
  },
  byApiKey: {
    key: (e) => e.apiKeyKey,
    name: (e) => e.keyName,
    detail: (e) => e.apiKeyMasked,
    itemName: (e) => e.provider,
    itemDetail: (e) => e.rawModel,
  },
  byEndpoint: {
    key: (e) => e.endpoint,
    name: (e) => e.endpoint,
    detail: () => "",
    itemName: (e) => e.provider,
    itemDetail: (e) => e.rawModel,
  },
};

function parseLog(line) {
  const parts = line.split(" | ");
  return {
    time: parts[0] || "",
    model: parts[1] || "",
    provider: parts[2] || "",
    account: parts[3] || "",
    key: parts[4] || "",
    inTokens: parts[5] || "",
    outTokens: parts[6] || "",
    status: parts[7] || "",
    extra: parts.slice(8).join(" | "),
  };
}

window.Alpine.data("usagePage", () => Object.assign({
  loading: true,
  error: "",
  role: "user",
  tab: "overview",
  period: "today",
  stats: emptyStats(),
  points: [],
  metric: "tokens",
  barMetric: "tokens",
  group: "byApiKey",
  open: {},
  costMode: false,
  sortKey: "requests",
  sortDesc: true,
  live: { pending: { byModel: {}, byAccount: {} }, activeRequests: [], recentRequests: [], errorProvider: "" },
  latency: {},
  activeCount: 0,
  activeProviders: [],
  lastProvider: "",
  seen: {},
  tick: 0,
  logLines: [],
  logCursor: "",
  logPaged: false,
  logFilter: "",
  logStatus: "all",
  logRefresh: true,
  gone: false,
  loadSeq: 0,
  usageSource: null,
  latencySource: null,
  clock: null,
  activeTimer: null,
  logTimer: null,

  init() {
    const query = new URLSearchParams(location.search);
    if (periods.indexOf(query.get("period")) >= 0) {
      this.period = query.get("period");
    }
    if (query.get("tab") === "logs") {
      this.tab = "logs";
    }
    this.clock = setInterval(() => this.tickOnce(), 1000);
    this.start();
  },

  async start() {
    await this.load();
    if (this.gone) {
      return;
    }
    if (this.isAdmin()) {
      this.connectUsage();
      this.connectLatency();
      this.pollActive();
    }
    if (this.tab === "logs") {
      this.loadLogs();
      this.startLogTimer();
    }
    this.$nextTick(() => this.drawAll());
  },

  isAdmin() {
    return this.role !== "user";
  },


  async load() {
    const seq = ++this.loadSeq;
    this.loading = true;
    this.error = "";
    try {
      this.role = (await session()).role;
      const prefix = "?period=" + encodeURIComponent(this.period);
      const stats = await api("/api/usage/stats" + prefix);
      const points = await api("/api/usage/chart" + prefix);
      if (seq !== this.loadSeq) {
        return;
      }
      this.stats = Object.assign(emptyStats(), stats);
      this.live.activeRequests = this.stats.activeRequests || [];
      this.live.recentRequests = this.stats.recentRequests || [];
      this.live.errorProvider = this.stats.errorProvider || "";
      this.live.pending = this.stats.pending || this.live.pending;
      this.trackActive();
      this.points = points;
    } catch (err) {
      if (seq === this.loadSeq) {
        this.error = err.message;
      }
    } finally {
      if (seq === this.loadSeq) {
        this.loading = false;
      }
    }
  },

  setPeriod(event) {
    this.period = event.target.value;
    this.syncURL();
    this.load().then(() => {
      if (this.gone) {
        return;
      }
      if (this.tab === "logs") {
        this.loadLogs();
      }
      this.$nextTick(() => this.drawAll());
    });
  },

  setTab(value) {
    this.tab = value;
    this.syncURL();
    if (value === "logs") {
      this.loadLogs();
      this.startLogTimer();
      return;
    }
    this.stopLogTimer();
    this.$nextTick(() => this.drawAll());
  },

  syncURL() {
    const query = new URLSearchParams({ period: this.period, tab: this.tab });
    history.replaceState({}, "", "/dashboard/usage?" + query.toString());
  },

  setMetric(event) {
    this.metric = event.target.value;
    this.$nextTick(() => this.drawTrend());
  },

  setBarMetric(event) {
    this.barMetric = event.target.value;
    this.$nextTick(() => this.drawBars());
  },

  setGroup(event) {
    this.group = event.target.value;
  },

  setCost(event) {
    this.costMode = event.target.value === "cost";
  },

  setSort(key) {
    if (this.sortKey === key) {
      this.sortDesc = !this.sortDesc;
    } else {
      this.sortKey = key;
      this.sortDesc = true;
    }
  },

  // groups is the breakdown under the chosen grouping: one summary row per key,
  // account, model or endpoint, each holding the models it spent on.
  groups() {
    const rules = groupBy[this.group] || groupBy.byModel;
    const merged = new Map();
    for (const entry of Object.values(this.stats[this.group] || {})) {
      const key = rules.key(entry) || "";
      let row = merged.get(key);
      if (!row) {
        row = { key: key, name: rules.name(entry) || t("usage.unknown"), detail: rules.detail(entry) || "", items: [] };
        for (const field of ["requests", "promptTokens", "cachedTokens", "completionTokens", "cost"]) {
          row[field] = 0;
        }
        merged.set(key, row);
      }
      const item = {
        key: row.items.length,
        name: rules.itemName(entry) || t("usage.unknown"),
        detail: rules.itemDetail(entry) || "",
        requests: entry.requests || 0,
        promptTokens: entry.promptTokens || 0,
        cachedTokens: entry.cachedTokens || 0,
        completionTokens: entry.completionTokens || 0,
        cost: entry.cost || 0,
      };
      row.items.push(item);
      for (const field of ["requests", "promptTokens", "cachedTokens", "completionTokens", "cost"]) {
        row[field] += item[field];
      }
    }
    const order = this.rowOrder();
    const rows = Array.from(merged.values());
    for (const row of rows) {
      row.total = row.promptTokens + row.completionTokens;
      for (const item of row.items) {
        item.total = item.promptTokens + item.completionTokens;
      }
      row.items.sort(order);
    }
    rows.sort(order);
    return rows;
  },

  // rowOrder compares by the header the reader chose; a name sorts as text and
  // every other column as a number.
  rowOrder() {
    const key = this.sortKey;
    const dir = this.sortDesc ? -1 : 1;
    return (a, b) => dir * (key === "name" ? a.name.localeCompare(b.name) : a[key] - b[key]);
  },

  // openItems is what shows under a summary row: its models while the row is
  // open, nothing while it is closed.
  openItems(row) {
    return this.open[this.group + ":" + row.key] === true ? row.items : [];
  },

  isOpen(row) {
    return this.open[this.group + ":" + row.key] === true;
  },

  // toggle copies the object: Alpine only sees a key change on a new reference.
  toggle(row) {
    const id = this.group + ":" + row.key;
    this.open = Object.assign({}, this.open, { [id]: !this.open[id] });
  },

  // sortLabel is what a column header's aria-sort says about its own column.
  sortLabel(key) {
    if (this.sortKey !== key) {
      return "none";
    }
    return this.sortDesc ? "descending" : "ascending";
  },

  costOrTotal() {
    return this.costMode ? "cost" : "total";
  },


  totalOf(row) {
    return row.promptTokens + row.completionTokens;
  },

  pendingOf() {
    return this.isAdmin() ? Object.values(this.live.pending.byModel || {}).reduce((sum, n) => sum + n, 0) : 0;
  },

  liveRequests() {
    let total = 0;
    for (const state of Object.values(this.latency)) {
      total += (state.successCount || 0) + (state.failCount || 0);
    }
    return total;
  },

  averageLatency() {
    const seen = Object.values(this.latency).filter((state) => state.emaLatency > 0);
    if (seen.length === 0) {
      return 0;
    }
    return Math.round(seen.reduce((sum, state) => sum + state.emaLatency, 0) / seen.length);
  },

  relative(value) {
    void this.tick;
    const at = new Date(value).getTime();
    if (isNaN(at)) {
      return value || "";
    }
    const seconds = Math.max(0, Math.round((Date.now() - at) / 1000));
    if (seconds < 60) {
      return seconds + "s";
    }
    if (seconds < 3600) {
      return Math.round(seconds / 60) + "m";
    }
    return new Date(at).toLocaleTimeString();
  },

  statusWord(status) {
    if (!status || status === "ok") {
      return t("usage.ok");
    }
    if (status === "pending") {
      return t("usage.pending");
    }
    // A log line carries "<code> <class>"; the recent list carries the class
    // alone and older rows a bare code.
    const parts = status.split(" ");
    const word = failureWords[parts[parts.length - 1]];
    if (word) {
      return (/^\d{3}$/.test(parts[0]) ? parts[0] + " " : "") + t(word);
    }
    return t("usage.failed") + " " + status;
  },

  muted(value) {
    return value === "-" ? "text-secondary" : "";
  },

  connectUsage() {
    const source = new EventSource("/api/usage/stream");
    this.usageSource = source;
    source.onmessage = (event) => {
      let frame;
      try {
        frame = JSON.parse(event.data);
      } catch (err) {
        return;
      }
      this.live.pending = frame.pending || this.live.pending;
      this.live.activeRequests = frame.activeRequests || [];
      this.live.recentRequests = frame.recentRequests || [];
      this.live.errorProvider = frame.errorProvider || "";
      this.trackActive();
      this.drawTopology();
    };
  },

  connectLatency() {
    const source = new EventSource("/api/health/latency-stream");
    this.latencySource = source;
    source.onmessage = (event) => {
      let frame;
      try {
        frame = JSON.parse(event.data);
      } catch (err) {
        return;
      }
      this.latency = frame.providers || {};
    };
  },

  async pollActive() {
    await this.readActive();
    if (this.gone) {
      return;
    }
    this.activeTimer = setInterval(() => this.readActive(), 5000);
  },

  async readActive() {
    if (document.hidden) {
      return;
    }
    try {
      const answer = await api("/api/usage/active-providers?period=" + encodeURIComponent(this.period));
      this.activeCount = answer.activeProviders || 0;
    } catch (err) {
      // A live count is a nicety; the totals above are the page.
    }
  },

  trackActive() {
    const now = Date.now();
    for (const row of this.live.activeRequests) {
      this.seen[row.provider] = now;
    }
    const recent = this.live.recentRequests;
    if (recent.length > 0) {
      this.lastProvider = recent[0].provider || "";
    }
    this.activeProviders = Object.keys(this.seen).filter((name) => now - this.seen[name] < 60000);
  },

  tickOnce() {
    this.tick++;
    if (this.isAdmin() && this.tab === "overview") {
      this.trackActive();
      this.drawTopology();
    }
  },

  // loadLogs reads the newest page, or the page after cursor and puts it above
  // what is already shown. The cursor is the header a full page carries.
  async loadLogs(before) {
    let path = "/api/usage/logs?period=" + encodeURIComponent(this.period);
    if (before) {
      path += "&before=" + encodeURIComponent(before);
    }
    try {
      const res = await fetch(path, { credentials: "same-origin" });
      const rows = await res.json().catch(() => null);
      if (!res.ok) {
        throw new Error((rows && rows.error) || "Request failed (" + res.status + ")");
      }
      const page = (rows || []).map(parseLog);
      this.logLines = before ? page.concat(this.logLines) : page;
      this.logPaged = Boolean(before);
      this.logCursor = res.headers.get("X-Next-Cursor") || "";
    } catch (err) {
      this.error = err.message;
    }
  },

  async loadOlder() {
    if (this.logCursor) {
      await this.loadLogs(this.logCursor);
    }
  },

  startLogTimer() {
    this.stopLogTimer();
    this.logTimer = setInterval(() => {
      // A refresh replaces the list, so it waits while older pages are open.
      if (this.logRefresh && !this.logPaged && !document.hidden) {
        this.loadLogs();
      }
    }, 3000);
  },

  stopLogTimer() {
    if (this.logTimer) {
      clearInterval(this.logTimer);
      this.logTimer = null;
    }
  },

  setLogFilter(event) {
    this.logFilter = event.target.value;
  },

  setLogStatus(event) {
    this.logStatus = event.target.value;
  },

  toggleLogRefresh(event) {
    this.logRefresh = event.target.checked;
  },

  logKind(row) {
    if (!row.status || row.status === "ok") {
      return "ok";
    }
    return row.status === "pending" ? "pending" : "failed";
  },

  logRows() {
    const q = this.logFilter.trim().toLowerCase();
    return this.logLines.filter((row) => {
      if (this.logStatus !== "all" && this.logKind(row) !== this.logStatus) {
        return false;
      }
      if (!q) {
        return true;
      }
      return [row.model, row.provider, row.account, row.key, row.extra].join(" ").toLowerCase().indexOf(q) >= 0;
    });
  },

  destroy() {
    this.gone = true;
    if (this.usageSource) {
      this.usageSource.close();
      this.usageSource = null;
    }
    if (this.latencySource) {
      this.latencySource.close();
      this.latencySource = null;
    }
    clearInterval(this.clock);
    clearInterval(this.activeTimer);
    this.stopLogTimer();
    this.dropCharts();
  },
}, usageChartMethods, formatMethods));
