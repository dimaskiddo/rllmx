// Endpoint & Keys: the address a client calls, the dashboard's settings that
// guard it, and this machine's API keys.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { copy } from "/static/js/ui/copy.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";
import { setPath } from "/static/js/ui/fields.js";

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

function emptyBudget() {
  const row = { day: "", month: "", total: "" };
  return { requests: Object.assign({}, row), tokens: Object.assign({}, row), cost: Object.assign({}, row) };
}

window.Alpine.data("endpointPage", () => Object.assign({
  loading: true,
  error: "",
  role: "admin",
  keys: [],
  users: [],
  settings: { requireApiKey: false, requireLogin: true },
  baseUrl: location.origin + "/v1",
  revealed: {},
  busy: {},
  create: { name: "", owner: "", error: "", busy: false },
  created: null,
  edit: null,
  userForm: null,
  choices: [],

  setField(path, event) {
    setPath(this, path, event.target.value);
  },

  isAdmin() {
    return this.role !== "user";
  },

  userName(id) {
    const found = this.users.find((u) => u.id === id);
    return found ? found.username : t("users.ownerNone");
  },

  async init() {
    await this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const status = await api("/api/auth/status").catch(() => ({}));
      this.role = status.role === "user" ? "user" : "admin";
      const listing = await api("/api/keys");
      this.keys = listing.keys || [];
      if (!this.isAdmin()) {
        return;
      }

      const [settings, aliases, combos, models, custom, catalog, providers, users] = await Promise.all([
        api("/api/settings"),
        api("/api/models/alias").catch(() => ({ aliases: {} })),
        api("/api/combos").catch(() => ({ combos: [] })),
        api("/api/models").catch(() => ({ models: [] })),
        api("/api/models/custom").catch(() => ({ models: [] })),
        api("/api/providers/catalog").catch(() => ({ providers: [] })),
        api("/api/providers").catch(() => ({ connections: [] })),
        api("/api/users").catch(() => ({ users: [] })),
      ]);
      this.settings = {
        requireApiKey: settings.requireApiKey === true,
        requireLogin: settings.requireLogin === true,
        hasPassword: settings.hasPassword === true,
      };
      this.users = users.users || [];
      const names = Object.keys(aliases.aliases || {}).concat((combos.combos || []).map((c) => c.name));
      const live = liveModels(models.models || [], custom.models || [], catalog.providers || [], providers.connections || []);
      this.choices = Array.from(new Set(names.concat(live))).sort();
      if (this.keys.length === 0) {
        await api("/api/keys", { method: "POST", body: { name: "Default Key" } });
        this.keys = (await api("/api/keys")).keys || [];
      }
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
      await api("/api/settings", { method: "PATCH", body: { requireApiKey: next } });
      this.settings.requireApiKey = next;
    } catch (err) {
      // The browser already flipped the box; :checked is not re-applied when the
      // saved value did not change, so put it back by hand.
      box.checked = this.settings.requireApiKey;
      toast().error(err.message);
    }
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
      const full = await api("/api/keys/" + encodeURIComponent(key.id));
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
        const full = await api("/api/keys/" + encodeURIComponent(key.id));
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
    const body = {
      name: this.edit.name,
      owner: this.edit.owner,
      isActive: this.edit.isActive,
      responseCache: this.edit.responseCache,
      access: { restricted: this.edit.restricted, allow: this.edit.allow },
      quota: this.quotaFromBudget(),
    };
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

  openUser(user) {
    this.userForm = {
      id: user ? user.id : "",
      name: user ? user.username : "",
      email: user ? user.email || "" : "",
      password: "",
      error: "",
      busy: false,
    };
    this.$refs.userDlg.showModal();
    this.$refs.userName.focus();
  },

  closeUser() {
    this.$refs.userDlg.close();
    this.userForm = null;
  },

  setUser(field, event) {
    this.userForm[field] = event.target.value;
  },

  async saveUser() {
    const form = this.userForm;
    form.error = "";
    form.busy = true;
    try {
      const body = { name: form.name, email: form.email };
      if (form.password !== "") {
        body.password = form.password;
      }
      if (form.id) {
        await api("/api/users/" + encodeURIComponent(form.id), { method: "PUT", body: body });
      } else {
        await api("/api/users", { method: "POST", body: body });
      }
      this.closeUser();
      await this.load();
      toast().success(t("users.saved"));
    } catch (err) {
      form.error = err.message;
    } finally {
      form.busy = false;
    }
  },

  async removeUser(user) {
    const yes = await ask({ title: t("users.deleteTitle"), message: t("users.deleteBody"), confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/users/" + encodeURIComponent(user.id), { method: "DELETE" });
      await this.load();
      toast().success(t("users.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },
}, formatMethods));
