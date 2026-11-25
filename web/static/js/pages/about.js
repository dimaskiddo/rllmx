// About: the build's version and commit, an update check the reader asks for,
// and the credits. The check makes one outbound call per click.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { session } from "/static/js/ui/session.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";

window.Alpine.data("aboutPage", () => Object.assign({
  loading: true,
  error: "",
  version: "",
  commit: "",
  check: { busy: false, done: false, error: "", latest: "", url: "", releasedOn: "", hasUpdate: false },
  admin: false,
  upgrading: false,
  upgradeError: "",

  init() {
    this.load();
  },

  async load() {
    try {
      const [out, who] = await Promise.all([api("/api/version"), session()]);
      this.version = out.currentVersion || "";
      this.commit = out.commit || "";
      this.admin = who.role !== "user";
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  async checkNow() {
    this.check.busy = true;
    this.check.done = false;
    this.check.error = "";
    try {
      const out = await api("/api/about/update");
      this.check.latest = out.latestVersion || "";
      this.check.url = out.url || "";
      this.check.hasUpdate = out.hasUpdate === true;
      this.check.releasedOn = (out.publishedAt || "").slice(0, 10);
      this.check.done = true;
    } catch (err) {
      this.check.error = err.message;
    } finally {
      this.check.busy = false;
    }
  },

  // upgrade replaces this binary and restarts it; the page is reloaded once the
  // new process answers.
  async upgrade() {
    this.upgradeError = "";
    const yes = await ask({ title: t("about.updateTitle"), message: t("about.updateBody"), danger: true, confirm: t("about.updateNow") });
    if (!yes || this.upgrading) {
      return;
    }
    this.upgrading = true;
    try {
      await api("/api/about/upgrade", { method: "POST" });
      toast().success(t("about.restarting"));
      setTimeout(() => location.reload(), 2500);
    } catch (err) {
      this.upgradeError = err.message;
      this.upgrading = false;
    }
  },
}, formatMethods));
