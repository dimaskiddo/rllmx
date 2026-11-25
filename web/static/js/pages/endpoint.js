// Endpoint & Keys: the address a client calls, the dashboard's settings that
// guard it, and this machine's API keys.
import { api, patchSettings } from "/static/js/ui/api.js";
import { session } from "/static/js/ui/session.js";
import { ask } from "/static/js/ui/dialog.js";
import { copy } from "/static/js/ui/copy.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";
import { setPath } from "/static/js/ui/fields.js";
import { fieldMethods } from "/static/js/ui/fields.js";
import { invalidateModelChoices, loadModelChoices } from "./model-choices.js";

function budgetWindowKey(window) {
  return { day: "keys.perDay", month: "keys.perMonth", total: "keys.allTime" }[window];
}

function budgetAmount(kind, value) {
  return kind === "cost" ? "$" + Number(value).toFixed(2) : Number(value).toLocaleString();
}

function emptyBudget() {
  const row = { day: "", month: "", total: "" };
  return { requests: Object.assign({}, row), tokens: Object.assign({}, row), cost: Object.assign({}, row) };
}

window.Alpine.data("endpointPage", () => Object.assign({
  loading: true,
  error: "",
  role: "user",
  keys: [],
  users: [],
  settings: { requireApiKey: false, requireLogin: true },
  baseUrl: location.origin + "/v1",
  revealed: {},
  busy: {},
  create: { name: "", owner: "", error: "", busy: false },
  created: null,
  edit: null,
  choices: [],


  isAdmin() {
    return this.role !== "user";
  },

  // ownerText is the address the owner signs in with, or their username when
  // they have none, so a key always says whose it is.
  ownerText(id) {
    const found = this.users.find((u) => u.id === id);
    return found ? found.email || found.username : t("users.ownerNone");
  },

  async init() {
    invalidateModelChoices();
    await this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      this.role = (await session()).role;
      const listing = await api("/api/keys");
      this.keys = listing.keys || [];
      if (!this.isAdmin()) {
        return;
      }

      const [settings, users, choices] = await Promise.all([
        api("/api/settings"),
        api("/api/users").catch(() => ({ users: [] })),
        loadModelChoices(),
      ]);
      this.settings = {
        requireApiKey: settings.requireApiKey === true,
        requireLogin: settings.requireLogin === true,
        hasPassword: settings.hasPassword === true,
      };
      this.users = users.users || [];
      this.choices = choices;
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  async copyBaseUrl() {
    await copy(this.baseUrl, null);
  },

  async toggleKeyRequired(event) {
    const box = event.target;
    const next = box.checked;
    try {
      await patchSettings({ requireApiKey: next });
      this.settings.requireApiKey = next;
    } catch (err) {
      // The browser already flipped the box; :checked is not re-applied when the
      // saved value did not change, so put it back by hand.
      box.checked = this.settings.requireApiKey;
      toast().error(err.message);
    }
  },

  // budgetMeters is one row per limited amount and window: what is left of the
  // budget, from 100% for a key that has spent nothing.
  budgetMeters(key) {
    const limit = key.quota || {};
    const spend = key.spend || {};
    const out = [];
    for (const kind of ["requests", "tokens", "cost"]) {
      const amounts = limit[kind] || {};
      for (const window of ["day", "month", "total"]) {
        const cap = Number(amounts[window]);
        if (!(cap > 0)) {
          continue;
        }
        const used = Number((spend[window] || {})[kind] || 0);
        const percent = Math.max(0, Math.min(100, ((cap - used) / cap) * 100));
        out.push({
          key: kind + "." + window,
          label: t("keys." + kind) + " · " + t(budgetWindowKey(window)),
          percent: percent,
          level: this.meterLevel(percent),
          text: budgetAmount(kind, used) + " / " + budgetAmount(kind, cap),
        });
      }
    }
    return out;
  },

  // limited is a key with a budget window spent to zero.
  limited(key) {
    return this.budgetMeters(key).some((meter) => meter.percent <= 0);
  },

  keyText(key) {
    return this.revealed[key.id] || key.key;
  },

  async reveal(key) {
    if (this.revealed[key.id]) {
      const next = Object.assign({}, this.revealed);
      delete next[key.id];
      this.revealed = next;
      return;
    }
    try {
      const full = await api("/api/keys/" + encodeURIComponent(key.id) + "/reveal", { method: "POST" });
      this.revealed = Object.assign({}, this.revealed, { [key.id]: full.key.key });
    } catch (err) {
      toast().error(err.message);
    }
  },

  // copyKey copies the value without putting it on screen; only the eye button
  // reveals a key.
  async copyKey(key) {
    let value = this.revealed[key.id] || "";
    if (!value) {
      try {
        const full = await api("/api/keys/" + encodeURIComponent(key.id) + "/reveal", { method: "POST" });
        value = full.key.key;
      } catch (err) {
        toast().error(err.message);
        return;
      }
    }
    await copy(value, null);
  },

  async toggleActive(key) {
    if (key.isActive) {
      const yes = await ask({ title: t("keys.pauseTitle"), message: t("keys.pauseBody"), danger: false });
      if (!yes) {
        return;
      }
    }
    this.busy[key.id] = true;
    try {
      await api("/api/keys/" + encodeURIComponent(key.id), { method: "PUT", body: { isActive: !key.isActive } });
      key.isActive = !key.isActive;
    } catch (err) {
      toast().error(err.message);
    } finally {
      this.busy[key.id] = false;
    }
  },

  async remove(key) {
    const yes = await ask({ title: t("keys.deleteTitle"), message: t("keys.deleteBody"), confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/keys/" + encodeURIComponent(key.id), { method: "DELETE" });
      this.keys = this.keys.filter((item) => item.id !== key.id);
      toast().success(t("keys.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },

  openCreate() {
    this.create = { name: "", owner: "", error: "", busy: false };
    this.$refs.createDlg.showModal();
    this.$refs.createName.focus();
  },

  closeCreate() {
    this.$refs.createDlg.close();
  },

  async createKey() {
    this.create.error = "";
    this.create.busy = true;
    try {
      const made = await api("/api/keys", { method: "POST", body: { name: this.create.name, owner: this.create.owner } });
      this.keys = (await api("/api/keys")).keys || [];
      this.created = made;
      this.closeCreate();
      this.$refs.createdDlg.showModal();
    } catch (err) {
      this.create.error = err.message;
    } finally {
      this.create.busy = false;
    }
  },

  closeCreated() {
    this.$refs.createdDlg.close();
    this.forgetCreated();
  },

  forgetCreated() {
    this.created = null;
  },

  async copyCreated() {
    await copy(this.created.key, null);
  },

  budgetFrom(quota) {
    const out = emptyBudget();
    for (const kind of ["requests", "tokens", "cost"]) {
      const amounts = quota && quota[kind];
      if (!amounts) {
        continue;
      }
      for (const window of ["day", "month", "total"]) {
        if (typeof amounts[window] === "number") {
          out[kind][window] = String(amounts[window]);
        }
      }
    }
    return out;
  },

  openEdit(key) {
    const access = key.access || { restricted: false, allow: [] };
    this.edit = {
      id: key.id,
      name: key.name,
      owner: key.owner || "",
      isActive: key.isActive,
      responseCache: key.responseCache === true,
      restricted: access.restricted === true,
      allow: (access.allow || []).slice(),
      add: "",
      budget: this.budgetFrom(key.quota),
      error: "",
      busy: false,
    };
    this.$refs.editDlg.showModal();
  },

  closeEdit() {
    this.$refs.editDlg.close();
    this.edit = null;
  },

  toggleRestricted() {
    this.edit.restricted = !this.edit.restricted;
  },

  toggleEditActive() {
    this.edit.isActive = !this.edit.isActive;
  },

  toggleEditCache() {
    this.edit.responseCache = !this.edit.responseCache;
  },

  setBudget(kind, window, event) {
    this.edit.budget[kind][window] = event.target.value;
  },

  addAllow() {
    const entry = this.edit.add.trim();
    if (!entry || this.edit.allow.indexOf(entry) >= 0) {
      this.edit.add = "";
      return;
    }
    this.edit.allow.push(entry);
    this.edit.add = "";
  },

  removeAllow(entry) {
    this.edit.allow = this.edit.allow.filter((item) => item !== entry);
  },

  quotaFromBudget() {
    const out = {};
    let any = false;
    for (const kind of ["requests", "tokens", "cost"]) {
      const amounts = {};
      for (const window of ["day", "month", "total"]) {
        const raw = this.edit.budget[kind][window];
        const value = Number(raw);
        if (raw !== "" && isFinite(value) && value >= 0) {
          amounts[window] = value;
          any = true;
        }
      }
      if (Object.keys(amounts).length > 0) {
        out[kind] = amounts;
      }
    }
    // An empty object is how the API clears a budget; null would leave it as it is.
    return any ? out : {};
  },

  async saveEdit() {
    this.edit.error = "";
    this.edit.busy = true;
    // A standard user may only rename: the server refuses the other fields.
    const body = this.isAdmin()
      ? {
        name: this.edit.name,
        owner: this.edit.owner,
        isActive: this.edit.isActive,
        responseCache: this.edit.responseCache,
        access: { restricted: this.edit.restricted, allow: this.edit.allow },
        quota: this.quotaFromBudget(),
      }
      : { name: this.edit.name };
    try {
      await api("/api/keys/" + encodeURIComponent(this.edit.id), { method: "PUT", body: body });
      this.closeEdit();
      await this.load();
      toast().success(t("keys.saved"));
    } catch (err) {
      this.edit.error = err.message;
    } finally {
      this.edit.busy = false;
    }
  },

}, formatMethods, fieldMethods));
