// Combos: an ordered model list with a routing strategy, and the suggest dialog
// that ranks live models into one.
import { api, patchSettings } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { copy } from "/static/js/ui/copy.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";
import { setPath } from "/static/js/ui/fields.js";
import { modelEditorMethods } from "./combo-models.js";
import { fieldMethods } from "/static/js/ui/fields.js";
import { loadModelChoices } from "./model-choices.js";

const emptyWeights = { reliability: 0.4, latency: 0.3, cost: 0.2, quality: 0.1 };

function emptyForm() {
  return { mode: "create", id: "", name: "", models: [], add: "", error: "", busy: false };
}

function emptySuggest() {
  return { weights: Object.assign({}, emptyWeights), prompt: "", limit: 7, name: "auto-recommended", busy: false, error: "", models: [], tested: [], summary: {}, };
}

window.Alpine.data("combosPage", () => Object.assign({
  loading: true,
  error: "",
  combos: [],
  choices: [],
  capsBy: {},
  adapter: null,
  adapterAdd: { vision: "", audioInput: "" },
  strategies: {},
  globalStrategy: "fallback",
  search: "",
  form: emptyForm(),
  suggest: emptySuggest(),
  suggestSeq: 0,


  async init() {
    await this.load();
  },

  // load shows "Loading" only the first time. A later load, after a save or a
  // switch, swaps the data under what is already on screen instead of blanking
  // the page and losing the reader's place.
  async load() {
    this.error = "";
    try {
      const [combos, settings, models, choices] = await Promise.all([
        api("/api/combos"),
        api("/api/settings"),
        api("/api/models").catch(() => ({ models: [] })),
        loadModelChoices(),
      ]);
      const capsBy = {};
      for (const m of models.models || []) {
        if (!m.caps) {
          continue;
        }
        for (const key of [m.fullModel, m.routedModel, m.alias, m.provider + "/" + m.model]) {
          if (key && !capsBy[key]) {
            capsBy[key] = m.caps;
          }
        }
      }
      this.capsBy = capsBy;
      this.combos = combos.combos || [];
      this.choices = choices;
      this.adapter = this.adapterFrom(settings.capacityAdapter);
      this.strategies = settings.comboStrategies || {};
      this.globalStrategy = settings.comboStrategy || "fallback";
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  visible() {
    const q = this.search.trim().toLowerCase();
    if (!q) {
      return this.combos;
    }
    return this.combos.filter((c) => c.name.toLowerCase().indexOf(q) >= 0 || c.models.some((m) => m.toLowerCase().indexOf(q) >= 0));
  },

  // adapterFrom reads the stored capacity adapter into the two blocks this page
  // offers. The other kinds it accepts stay as stored and are not edited here.
  adapterFrom(raw) {
    const out = { vision: { enabled: false, roundRobin: false, models: [] }, audioInput: { enabled: false, roundRobin: false, models: [] } };
    for (const kind of Object.keys(out)) {
      const entry = raw && raw[kind];
      if (!entry) {
        continue;
      }
      out[kind].enabled = entry.enabled === true;
      out[kind].roundRobin = entry.roundRobin === true;
      out[kind].models = Array.isArray(entry.models) ? entry.models.slice() : [];
    }
    return out;
  },

  adapterName(kind) {
    return kind === "vision" ? t("models.vision") : t("models.audio");
  },

  adapterBlock(kind) {
    return this.adapter ? this.adapter[kind] : { enabled: false, roundRobin: false, models: [] };
  },

  // commitAdapter saves the whole row. A refused save puts the page back to what
  // the store holds.
  async commitAdapter(before) {
    const body = {};
    for (const kind of Object.keys(this.adapter)) {
      const block = this.adapter[kind];
      if (!block.enabled && block.models.length === 0) {
        continue;
      }
      body[kind] = { enabled: block.enabled, roundRobin: block.roundRobin, models: block.models };
    }
    try {
      await patchSettings({ capacityAdapter: body });
    } catch (err) {
      this.adapter = before;
      toast().error(err.message);
    }
  },

  async toggleAdapter(kind, key, event) {
    const before = JSON.parse(JSON.stringify(this.adapter));
    this.adapter = Object.assign({}, this.adapter, {
      [kind]: Object.assign({}, this.adapter[kind], { [key]: event.target.checked }),
    });
    await this.commitAdapter(before);
  },

  setAdapterAdd(kind, event) {
    this.adapterAdd = Object.assign({}, this.adapterAdd, { [kind]: event.target.value });
  },

  async addAdapterModel(kind) {
    const value = (this.adapterAdd[kind] || "").trim();
    if (!value || this.adapter[kind].models.indexOf(value) >= 0) {
      return;
    }
    const before = JSON.parse(JSON.stringify(this.adapter));
    this.adapter = Object.assign({}, this.adapter, {
      [kind]: Object.assign({}, this.adapter[kind], { models: this.adapter[kind].models.concat([value]) }),
    });
    this.adapterAdd = Object.assign({}, this.adapterAdd, { [kind]: "" });
    await this.commitAdapter(before);
  },

  async removeAdapterModel(kind, index) {
    const before = JSON.parse(JSON.stringify(this.adapter));
    const models = this.adapter[kind].models.slice();
    models.splice(index, 1);
    this.adapter = Object.assign({}, this.adapter, { [kind]: Object.assign({}, this.adapter[kind], { models: models }) });
    await this.commitAdapter(before);
  },

  async moveAdapterModel(kind, index, delta) {
    const models = this.adapter[kind].models.slice();
    const to = index + delta;
    if (to < 0 || to >= models.length) {
      return;
    }
    const before = JSON.parse(JSON.stringify(this.adapter));
    const moved = models.splice(index, 1)[0];
    models.splice(to, 0, moved);
    this.adapter = Object.assign({}, this.adapter, { [kind]: Object.assign({}, this.adapter[kind], { models: models }) });
    await this.commitAdapter(before);
  },

  // capsOf is the member's declared capabilities; a name the model list does not
  // know is plain text until it does.
  capsOf(member) {
    return this.capsBy[member] || null;
  },

  // comboCaps is the union of its members: a combo reads a picture when one of
  // them does.
  comboCaps(combo) {
    const out = { vision: false, reasoning: false, audio: false };
    for (const member of combo.models || []) {
      const caps = this.capsBy[member];
      if (!caps) {
        continue;
      }
      out.vision = out.vision || caps.vision === true;
      out.reasoning = out.reasoning || caps.reasoning === true;
      out.audio = out.audio || caps.audio === true;
    }
    return out;
  },

  async copyName(combo) {
    await copy(combo.name, null);
  },

  modelsPreview(combo) {
    return combo.models.slice(0, 3);
  },

  moreCount(combo) {
    return Math.max(0, combo.models.length - 3);
  },

  entryFor(combo) {
    const entry = this.strategies[combo.name];
    return entry && typeof entry === "object" ? entry : {};
  },

  strategyOf(combo) {
    return this.entryFor(combo).fallbackStrategy || "";
  },

  policyOf(combo) {
    return this.entryFor(combo).smartPolicy || "smart";
  },

  thresholdOf(combo) {
    const value = this.entryFor(combo).confidenceThreshold;
    return value ? Math.round(value * 100) : 80;
  },

  routerOf(combo) {
    return this.entryFor(combo).routerModel || "";
  },

  judgeOf(combo) {
    return this.entryFor(combo).judgeModel || "";
  },

  // writeEntry merges one combo's entry and drops it when it is empty, so a
  // combo on the global default stores nothing and the selector cannot drift.
  async writeEntry(combo, patch) {
    try {
      const fresh = await api("/api/settings");
      this.strategies = fresh.comboStrategies || {};
    } catch (err) {
      toast().error(err.message);
      return false;
    }
    const next = Object.assign({}, this.strategies);
    const entry = Object.assign({}, this.entryFor(combo), patch);
    for (const key of Object.keys(entry)) {
      if (entry[key] === "" || entry[key] === null) {
        delete entry[key];
      }
    }
    if (Object.keys(entry).length === 0) {
      delete next[combo.name];
    } else {
      next[combo.name] = entry;
    }
    return this.saveStrategies(next);
  },

  async saveStrategies(next) {
    try {
      const settings = await patchSettings({ comboStrategies: next });
      this.adapter = this.adapterFrom(settings.capacityAdapter);
      this.strategies = settings.comboStrategies || {};
      this.globalStrategy = settings.comboStrategy || "fallback";
      return true;
    } catch (err) {
      toast().error(err.message);
      return false;
    }
  },

  // restore puts a control back to the stored value after a failed save: the
  // state did not change, so nothing would re-apply it.
  restore(event, value) {
    event.target.value = value;
  },

  async setStrategy(combo, event) {
    if (!(await this.writeEntry(combo, { fallbackStrategy: event.target.value }))) {
      this.restore(event, this.strategyOf(combo));
    }
  },

  async setPolicy(combo, event) {
    const value = event.target.value;
    const patch = { smartPolicy: value, sessionSticky: value !== "per-turn" };
    if (value === "smart") {
      patch.confidenceThreshold = 0.8;
    } else if (value === "custom") {
      patch.confidenceThreshold = this.thresholdOf(combo) / 100;
    }
    if (!(await this.writeEntry(combo, patch))) {
      this.restore(event, this.policyOf(combo));
    }
  },

  async setThreshold(combo, event) {
    const n = parseInt(event.target.value, 10);
    if (isNaN(n) || n < 1 || n > 100 || !(await this.writeEntry(combo, { confidenceThreshold: n / 100 }))) {
      this.restore(event, this.thresholdOf(combo));
    }
  },

  async setRouter(combo, event) {
    if (!(await this.writeEntry(combo, { routerModel: event.target.value.trim() }))) {
      this.restore(event, this.routerOf(combo));
    }
  },

  async setJudge(combo, event) {
    if (!(await this.writeEntry(combo, { judgeModel: event.target.value.trim() }))) {
      this.restore(event, this.judgeOf(combo));
    }
  },

  openCreate() {
    this.form = emptyForm();
    this.$refs.form.showModal();
    this.$nextTick(() => this.sortModels());
  },

  openEdit(combo) {
    this.form = { mode: "edit", id: combo.id, name: combo.name, models: combo.models.slice(), add: "", error: "", busy: false };
    this.$refs.form.showModal();
    this.$nextTick(() => this.sortModels());
  },

  closeForm() {
    this.stopSortable();
    this.form = emptyForm();
    this.$refs.form.close();
  },

  async saveForm() {
    this.form.error = "";
    const name = this.form.name.trim();
    if (!name) {
      this.form.error = t("combos.nameRequired");
      return;
    }
    if (!/^[A-Za-z0-9_.-]+$/.test(name)) {
      this.form.error = t("combos.nameShape");
      return;
    }
    this.form.busy = true;
    try {
      const body = { name: name, models: this.form.models };
      if (this.form.mode === "edit") {
        await api("/api/combos/" + encodeURIComponent(this.form.id), { method: "PUT", body: body });
      } else {
        await api("/api/combos", { method: "POST", body: body });
      }
      this.closeForm();
      await this.load();
      toast().success(t("combos.saved"));
    } catch (err) {
      this.form.error = err.message;
    } finally {
      this.form.busy = false;
    }
  },

  async removeCombo(combo) {
    const yes = await ask({ title: t("combos.deleteTitle"), message: t("combos.deleteBody") + " " + combo.name, danger: true, confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/combos/" + encodeURIComponent(combo.id), { method: "DELETE" });
      await this.load();
      toast().success(t("combos.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },

  openSuggest() {
    this.suggest = emptySuggest();
    this.$refs.suggest.showModal();
    this.runSuggest(false);
  },

  closeSuggest() {
    this.$refs.suggest.close();
  },

  onSuggestClosed() {
    this.suggest.busy = false;
  },

  setLimit(event) {
    this.suggest.limit = event.target.value;
    this.runSuggest(false);
  },

  setWeight(key, event) {
    this.suggest.weights[key] = parseFloat(event.target.value);
  },

  weightQuery() {
    const w = this.suggest.weights;
    return [w.reliability, w.latency, w.cost, w.quality].join(",");
  },

  async runSuggest(force) {
    const seq = ++this.suggestSeq;
    this.suggest.busy = true;
    this.suggest.error = "";
    try {
      const params = new URLSearchParams({ weights: this.weightQuery(), limit: String(this.suggest.limit) });
      if (this.suggest.prompt.trim()) {
        params.set("prompt", this.suggest.prompt.trim());
      }
      if (force) {
        params.set("force", "true");
      }
      const found = await api("/api/combos/suggest?" + params.toString());
      if (seq !== this.suggestSeq) {
        return;
      }
      this.suggest.models = found.models || [];
      this.suggest.tested = found.tested || [];
      this.suggest.summary = found.summary || {};
    } catch (err) {
      if (seq === this.suggestSeq) {
        this.suggest.error = err.message;
      }
    } finally {
      if (seq === this.suggestSeq) {
        this.suggest.busy = false;
      }
    }
  },

  excluded() {
    return this.suggest.tested.filter((r) => !r.ok);
  },

  async acceptSuggest() {
    this.suggest.error = "";
    this.suggest.busy = true;
    try {
      const created = await api("/api/combos/suggest/accept", { method: "POST", body: { name: this.suggest.name.trim(), models: this.suggest.models } });
      this.closeSuggest();
      await this.load();
      toast().success(t("combos.saved"));
      return created;
    } catch (err) {
      this.suggest.error = err.message;
    } finally {
      this.suggest.busy = false;
    }
  },

  async copyModels(combo) {
    await copy(combo.models.join("\n"));
  },

  destroy() {
    this.stopSortable();
  },
}, modelEditorMethods, formatMethods, fieldMethods));
