// Quota: one card per connection a quota reading answers for, with the windows
// the provider reports, the reset credits and the active switch.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";
import { clampInt } from "/static/js/ui/fields.js";

const quotaPageSize = 10;

// readQuota reads one connection's limits without the shared api() helper: a
// 401 here is the provider refusing the stored credential, not the dashboard
// session ending, and api() would send the browser to the sign-in page.
async function readQuota(path) {
  const res = await fetch(path, { credentials: "same-origin" });
  const data = await res.json().catch(() => null);
  if (res.status === 401) {
    const err = new Error((data && data.error) || t("quota.expired"));
    err.status = 401;
    throw err;
  }
  if (!res.ok) {
    throw new Error((data && data.error) || "Request failed (" + res.status + ")");
  }
  return data;
}

function emptyReading() {
  return { loading: true, error: "", message: "", quotas: {}, resetCredits: { availableCount: 0, credits: [] }, page: 1 };
}

window.Alpine.data("quotaPage", () => Object.assign({
  loading: true,
  error: "",
  connections: [],
  providerOptions: [],
  pagination: { page: 1, pageSize: 10, total: 0, totalPages: 0 },
  provider: "all",
  accountStatus: "all",
  readings: {},
  autoRefresh: true,
  countdown: 60,
  timer: null,
  tick: 0,
  gone: false,
  pass: 0,
  resetting: {},

  init() {
    this.load();
    this.timer = setInterval(() => this.beat(), 1000);
  },

  beat() {
    this.tick++;
    if (document.hidden || !this.autoRefresh) {
      return;
    }
    this.countdown--;
    if (this.countdown <= 0) {
      this.countdown = 60;
      this.readAll(false);
    }
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const query = new URLSearchParams({
        page: String(this.pagination.page),
        pageSize: String(this.pagination.pageSize),
        accountStatus: this.accountStatus,
        sort: "priority",
        provider: this.provider,
      });
      const answer = await api("/api/providers/client?" + query.toString());
      this.connections = answer.connections || [];
      this.providerOptions = answer.providerOptions || [];
      this.pagination = answer.pagination || this.pagination;
      this.readings = {};
      for (const conn of this.connections) {
        this.readings[conn.id] = emptyReading();
      }
      await this.readAll(false);
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  async readAll(force) {
    // One at a time: a reading is a provider call, and twenty at once is how a
    // dashboard trips a rate limit it then has to read past.
    const pass = ++this.pass;
    for (const conn of this.connections) {
      if (this.gone || pass !== this.pass) {
        return;
      }
      await this.read(conn, force);
    }
  },

  async read(conn, force) {
    const reading = this.readings[conn.id] || emptyReading();
    reading.loading = true;
    reading.error = "";
    this.readings = Object.assign({}, this.readings, { [conn.id]: reading });
    try {
      const answer = await readQuota("/api/usage/" + encodeURIComponent(conn.id) + (force ? "?force=1" : ""));
      const next = Object.assign(emptyReading(), {
        loading: false,
        message: answer.message || "",
        quotas: answer.quotas || {},
        resetCredits: answer.resetCredits || { availableCount: 0, credits: [] },
        page: reading.page || 1,
      });
      this.readings = Object.assign({}, this.readings, { [conn.id]: next });
    } catch (err) {
      const next = Object.assign({}, reading, { loading: false, error: err.status === 401 ? err.message : "" });
      this.readings = Object.assign({}, this.readings, { [conn.id]: next });
    }
  },

  rows(conn) {
    const reading = this.readings[conn.id];
    if (!reading) {
      return [];
    }
    return Object.entries(reading.quotas || {}).map(([key, row]) => ({
      key: key,
      name: row.displayName || key,
      used: row.used,
      total: row.total,
      percent: Math.max(0, Math.min(100, row.remainingPercentage)),
      unlimited: row.unlimited === true,
      credit: row.isCreditBalance === true,
      currency: row.currency || "",
      unit: row.unit || "",
      resetAt: row.resetAt || "",
    }));
  },

  shown(conn) {
    const reading = this.readings[conn.id];
    const page = reading ? reading.page || 1 : 1;
    const rows = this.rows(conn);
    const from = (page - 1) * quotaPageSize;
    return rows.slice(from, from + quotaPageSize);
  },

  quotaPages(conn) {
    return Math.max(1, Math.ceil(this.rows(conn).length / quotaPageSize));
  },

  quotaPage(conn) {
    const reading = this.readings[conn.id];
    return reading ? reading.page || 1 : 1;
  },

  turnPage(conn, delta) {
    const reading = this.readings[conn.id] || emptyReading();
    const next = clampInt(this.quotaPage(conn) + delta, 1, this.quotaPages(conn));
    reading.page = next;
    this.readings = Object.assign({}, this.readings, { [conn.id]: reading });
  },

  level(row) {
    if (row.unlimited) {
      return "";
    }
    if (row.percent < 30) {
      return "low";
    }
    return row.percent < 70 ? "mid" : "";
  },

  amount(row) {
    if (row.unlimited) {
      return t("quota.unlimited");
    }
    if (row.credit) {
      return (row.currency ? row.currency + " " : "") + this.num(row.used) + (row.unit ? " " + row.unit : "");
    }
    return this.num(row.used) + " / " + this.num(row.total) + (row.unit ? " " + row.unit : "");
  },

  resetText(row) {
    void this.tick;
    if (!row.resetAt) {
      return "";
    }
    const left = new Date(row.resetAt).getTime() - Date.now();
    if (isNaN(left) || left <= 0) {
      return t("quota.resetsNow");
    }
    const minutes = Math.round(left / 60000);
    if (minutes < 60) {
      return t("quota.resetsIn") + " " + minutes + "m";
    }
    const hours = Math.floor(minutes / 60);
    if (hours < 24) {
      return t("quota.resetsIn") + " " + hours + "h " + (minutes % 60) + "m";
    }
    return t("quota.resetsIn") + " " + Math.floor(hours / 24) + "d " + (hours % 24) + "h";
  },



  connectionName(conn) {
    return conn.displayName || conn.name || conn.email || conn.provider;
  },

  resetCredits(conn) {
    const reading = this.readings[conn.id];
    return reading && reading.resetCredits ? reading.resetCredits.availableCount || 0 : 0;
  },

  canReset(conn) {
    return (conn.provider === "claude" || conn.provider === "codex") && this.resetCredits(conn) > 0;
  },

  async reset(conn) {
    const left = this.resetCredits(conn);
    const yes = await ask({ title: t("quota.resetTitle"), message: t("quota.resetBody") + " " + left, danger: true, confirm: t("quota.reset") });
    if (!yes || this.resetting[conn.id]) {
      return;
    }
    this.resetting = Object.assign({}, this.resetting, { [conn.id]: true });
    try {
      if (conn.provider === "claude") {
        await api("/api/usage/" + encodeURIComponent(conn.id) + "/claude-reset", { method: "POST", body: {} });
      } else {
        await api("/api/usage/" + encodeURIComponent(conn.id) + "/codex-reset-credits", { method: "POST", body: {} });
      }
      toast().success(t("quota.resetDone"));
      await this.read(conn, true);
    } catch (err) {
      toast().error(err.message);
    } finally {
      this.resetting = Object.assign({}, this.resetting, { [conn.id]: false });
    }
  },

  async toggleActive(conn) {
    try {
      await api("/api/providers/" + encodeURIComponent(conn.id), { method: "PUT", body: { isActive: conn.isActive !== true } });
      conn.isActive = conn.isActive !== true;
    } catch (err) {
      toast().error(err.message);
    }
  },

  setProvider(event) {
    this.provider = event.target.value;
    this.pagination.page = 1;
    this.load();
  },

  setStatus(event) {
    this.accountStatus = event.target.value;
    this.pagination.page = 1;
    this.load();
  },

  setPageSize(event) {
    this.pagination.pageSize = parseInt(event.target.value, 10) || 10;
    this.pagination.page = 1;
    this.load();
  },

  turnClientPage(delta) {
    this.pagination.page = clampInt(this.pagination.page + delta, 1, this.pagination.totalPages || 1);
    this.load();
  },

  toggleAuto(event) {
    this.autoRefresh = event.target.checked;
    this.countdown = 60;
  },

  async refreshAll() {
    this.countdown = 60;
    await this.readAll(true);
  },

  destroy() {
    this.gone = true;
    clearInterval(this.timer);
  },
}, formatMethods));
