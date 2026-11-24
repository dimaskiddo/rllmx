// Pricing: one table per provider, five rates per model in dollars per million
// tokens. Only the providers a person touched are sent.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";

const fields = ["input", "output", "cached", "reasoning", "cache_creation"];

window.Alpine.data("pricingPage", () => Object.assign({
  loading: true,
  error: "",
  busy: false,
  search: "",
  table: {},
  dirty: {},

  init() {
    this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      this.table = await api("/api/pricing");
      this.dirty = {};
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  fields() {
    return fields;
  },

  fieldName(field) {
    return t("pricing." + field);
  },

  providers() {
    const q = this.search.trim().toLowerCase();
    const out = [];
    for (const name of Object.keys(this.table || {}).sort()) {
      const rows = [];
      for (const model of Object.keys(this.table[name] || {}).sort()) {
        if (q && name.toLowerCase().indexOf(q) < 0 && model.toLowerCase().indexOf(q) < 0) {
          continue;
        }
        rows.push(Object.assign({ model: model }, this.table[name][model]));
      }
      if (rows.length > 0 || !q) {
        out.push({ name: name, models: rows });
      }
    }
    return out;
  },

  modelCount() {
    let total = 0;
    for (const models of Object.values(this.table || {})) {
      total += Object.keys(models || {}).length;
    }
    return total;
  },

  providerCount() {
    return Object.keys(this.table || {}).length;
  },

  dirtyCount() {
    return Object.keys(this.dirty).length;
  },

  setSearch(event) {
    this.search = event.target.value;
  },

  setPrice(provider, model, field, event) {
    const value = parseFloat(event.target.value);
    if (isNaN(value) || value < 0) {
      return;
    }
    this.table[provider][model][field] = value;
    this.dirty = Object.assign({}, this.dirty, { [provider]: true });
  },

  async save() {
    const body = {};
    for (const provider of Object.keys(this.dirty)) {
      body[provider] = this.table[provider];
    }
    if (Object.keys(body).length === 0) {
      return;
    }
    this.busy = true;
    try {
      this.table = await api("/api/pricing", { method: "PATCH", body: body });
      this.dirty = {};
      toast().success(t("pricing.saved"));
    } catch (err) {
      this.error = err.message;
    } finally {
      this.busy = false;
    }
  },

  async reset() {
    const yes = await ask({ title: t("pricing.resetTitle"), message: t("pricing.resetBody"), danger: true, confirm: t("pricing.reset") });
    if (!yes) {
      return;
    }
    this.busy = true;
    try {
      this.table = await api("/api/pricing", { method: "DELETE" });
      this.dirty = {};
      toast().success(t("pricing.resetDone"));
    } catch (err) {
      this.error = err.message;
    } finally {
      this.busy = false;
    }
  },

}, formatMethods));
