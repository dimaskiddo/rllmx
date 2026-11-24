// Console Log: the redacted ring the logger already fills. The stream carries
// the ring on connect and then batches, so the page never reads the log file.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";

const cap = 200;

window.Alpine.data("consoleLogPage", () => Object.assign({
  lines: [],
  filter: "",
  source: null,

  init() {
    this.connect();
  },

  connect() {
    const source = new EventSource("/api/translator/console-logs/stream");
    this.source = source;
    source.onmessage = (event) => this.receive(event);
  },

  receive(event) {
    let frame;
    try {
      frame = JSON.parse(event.data);
    } catch (err) {
      return;
    }
    if (frame.type === "clear") {
      this.lines = [];
      return;
    }
    if (frame.type !== "init" && frame.type !== "lines") {
      return;
    }
    const follow = this.atTail();
    this.lines = (frame.type === "init" ? frame.logs || [] : this.lines.concat(frame.lines || [])).slice(-cap);
    if (follow) {
      this.$nextTick(() => this.toTail());
    }
  },

  // atTail is true while the reader has not scrolled up to read something older,
  // and on the first batch, when the panel is still empty. It is read before the
  // new lines render, because the lines themselves make the panel taller.
  atTail() {
    const panel = this.$refs.panel;
    if (!panel || this.lines.length === 0) {
      return true;
    }
    return panel.scrollHeight - panel.scrollTop - panel.clientHeight < 48;
  },

  toTail() {
    const panel = this.$refs.panel;
    if (panel) {
      panel.scrollTop = panel.scrollHeight;
    }
  },

  setFilter(event) {
    this.filter = event.target.value;
  },

  visible() {
    const q = this.filter.trim().toLowerCase();
    if (!q) {
      return this.lines;
    }
    return this.lines.filter((row) => String(row.message).toLowerCase().indexOf(q) >= 0);
  },

  lineText(row) {
    return (row.at || "") + " " + String(row.level || "info").toUpperCase() + " " + (row.message || "");
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
