// Console Log: the redacted ring the logger already fills, shown as a terminal.
// The stream carries the ring on connect and then batches, so the page never
// reads the log file.
import { api } from "/static/js/ui/api.js";
import { copy } from "/static/js/ui/copy.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";

const cap = 500;

// rank orders the levels the logger writes, so "warning" also shows errors.
const rank = { debug: 0, info: 1, warning: 2, error: 3, fatal: 3, panic: 3 };

window.Alpine.data("consoleLogPage", () => Object.assign({
  lines: [],
  filter: "",
  level: "all",
  source: null,
  // state is "connecting", "live" or "reconnecting"; the browser retries a
  // dropped stream by itself, so the page only says what it sees.
  state: "connecting",
  following: true,

  init() {
    this.connect();
  },

  connect() {
    const source = new EventSource("/api/translator/console-logs/stream");
    this.source = source;
    source.onopen = () => {
      this.state = "live";
    };
    source.onerror = () => {
      this.state = "reconnecting";
    };
    source.onmessage = (event) => this.receive(event);
  },

  receive(event) {
    let frame;
    try {
      frame = JSON.parse(event.data);
    } catch (err) {
      return;
    }
    this.state = "live";
    if (frame.type === "clear") {
      this.lines = [];
      return;
    }
    if (frame.type !== "init" && frame.type !== "lines") {
      return;
    }
    this.lines = (frame.type === "init" ? frame.logs || [] : this.lines.concat(frame.lines || [])).slice(-cap);
    if (this.following) {
      this.$nextTick(() => this.toTail());
    }
  },

  // onScroll keeps "following" true only while the reader is at the bottom, so
  // scrolling up to read something older stops the page from pulling them back.
  onScroll() {
    const panel = this.$refs.panel;
    if (panel) {
      this.following = panel.scrollHeight - panel.scrollTop - panel.clientHeight < 24;
    }
  },

  toTail() {
    const panel = this.$refs.panel;
    if (panel) {
      panel.scrollTop = panel.scrollHeight;
    }
  },

  jump() {
    this.following = true;
    this.toTail();
  },

  stateText() {
    return t("console.state." + this.state);
  },

  setFilter(event) {
    this.filter = event.target.value;
  },

  setLevel(event) {
    this.level = event.target.value;
    this.$nextTick(() => this.following && this.toTail());
  },

  visible() {
    const q = this.filter.trim().toLowerCase();
    const min = this.level === "all" ? 0 : rank[this.level];
    return this.lines.filter((row) => (rank[row.level] ?? 1) >= min && (!q || String(row.message).toLowerCase().indexOf(q) >= 0));
  },

  // clock is the time of day; the date is the same for a whole session of
  // reading and costs the line width.
  clock(row) {
    const at = new Date(row.at);
    return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString([], { hour12: false });
  },

  // levelText is the level as a word, so it reads without the colour.
  levelText(row) {
    return String(row.level || "info").toUpperCase().slice(0, 5);
  },

  lineText(row) {
    return this.clock(row) + " " + String(row.level || "info").toUpperCase() + " " + (row.message || "");
  },

  async copyLogs() {
    await copy(this.visible().map((row) => this.lineText(row)).join("\n"), this.$refs.panel);
  },

  async clearLogs() {
    const yes = await ask({ title: t("console.clearTitle"), message: t("console.clearBody"), danger: true, confirm: t("console.clear") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/translator/console-logs", { method: "DELETE" });
      this.lines = [];
    } catch (err) {
      toast().error(err.message);
    }
  },

  destroy() {
    if (this.source) {
      this.source.close();
      this.source = null;
    }
  },
}, formatMethods));
