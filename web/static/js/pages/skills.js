// Skills: the installed list, each skill's switch and config fields, the
// catalog sync and the auto-plan orchestration.
import { api, patchSettings } from "/static/js/ui/api.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { clampInt, setPath } from "/static/js/ui/fields.js";
import { studioMethods } from "./skills-studio.js";
import { formatMethods } from "/static/js/ui/format.js";

const hooks = ["all", "system-prompt", "pre-route", "pre-request"];

function planForm(settings) {
  return {
    autoPlanEnabled: settings.autoPlanEnabled === true,
    autoPlanMode: settings.autoPlanMode || "auto",
    autoPlanComboId: settings.autoPlanComboId || "",
    autoCodeComboId: settings.autoCodeComboId || "",
    autoPlanComplexityThreshold: settings.autoPlanComplexityThreshold || 6,
    autoPlanSmartClassify: settings.autoPlanSmartClassify === true,
    autoPlanMaxTokens: settings.autoPlanMaxTokens || 800,
    autoPlanTimeoutMs: settings.autoPlanTimeoutMs || 15000,
    autoPlanShowInResponse: settings.autoPlanShowInResponse === true,
  };
}

window.Alpine.data("skillsPage", () => Object.assign({
  loading: true,
  error: "",
  skills: [],
  problems: [],
  settings: {},
  combos: [],
  search: "",
  hook: "all",
  sync: { source: "", tag: "main", force: false, busy: false, result: "", error: "" },
  plan: planForm({}),
  studio: null,

  init() {
    this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const [list, settings, combos] = await Promise.all([api("/api/skills"), api("/api/settings"), api("/api/combos")]);
      this.skills = list.skills || [];
      this.problems = list.problems || [];
      this.settings = settings;
      this.plan = planForm(settings);
      this.combos = combos.combos || [];
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  hooks() {
    return hooks;
  },

  hookName(value) {
    return t("skills.hook." + value);
  },

  visible() {
    const q = this.search.trim().toLowerCase();
    return this.skills.filter((skill) => {
      if (this.hook !== "all" && skill.hook !== this.hook) {
        return false;
      }
      if (!q) {
        return true;
      }
      return (skill.name + " " + skill.id + " " + (skill.description || "")).toLowerCase().indexOf(q) >= 0;
    });
  },

  setSearch(event) {
    this.search = event.target.value;
  },

  setHookFilter(event) {
    this.hook = event.target.value;
  },

  // fieldKey is the settings row a config field lives under: its legacy key, or
  // the skill id and the field key.
  fieldKey(skill, field) {
    return field.legacy_key || skill.id + "_" + field.key;
  },

  // fieldValue is what the server stored for a field, else the manifest default.
  fieldValue(skill, field) {
    const stored = skill.values && skill.values[field.key];
    return stored !== undefined ? stored : field.default;
  },

  async patch(body) {
    try {
      this.settings = await patchSettings(body);
      return true;
    } catch (err) {
      toast().error(err.message);
      return false;
    }
  },

  async toggle(skill, event) {
    const box = event.target;
    if (await this.patch({ [skill.id + "Enabled"]: box.checked })) {
      skill.enabled = box.checked;
    } else {
      box.checked = !box.checked;
    }
  },

  setMode(skill, event) {
    this.patch({ [skill.id + "RoutingMode"]: event.target.value });
  },

  async setConfig(skill, field, event) {
    let value = event.target.value;
    if (field.type === "toggle") {
      value = event.target.checked;
    } else if (field.type === "number" || field.type === "slider") {
      value = Number(value);
      if (isNaN(value)) {
        return;
      }
    }
    if (await this.patch({ [this.fieldKey(skill, field)]: value })) {
      this.remember(skill, field, value);
    } else {
      await this.load();
    }
  },

  async setOption(skill, field, value) {
    if (await this.patch({ [this.fieldKey(skill, field)]: value })) {
      this.remember(skill, field, value);
    } else {
      await this.load();
    }
  },

  // remember keeps the page's copy of a saved value, so the field shows what the
  // server holds without a second read.
  remember(skill, field, value) {
    skill.values = Object.assign({}, skill.values, { [field.key]: value });
  },

  source(skill) {
    return skill.source === "custom" ? t("skills.custom") : skill.source || "";
  },

  setSync(path, event) {
    setPath(this.sync, path, event.target.value);
  },

  setSyncForce(event) {
    this.sync.force = event.target.checked;
  },

  async runSync() {
    this.sync.error = "";
    this.sync.result = "";
    this.sync.busy = true;
    try {
      const answer = await api("/api/skills/sync", { method: "POST", body: { source: this.sync.source.trim(), tag: this.sync.tag.trim(), force: this.sync.force } });
      this.sync.result = answer.total + " / " + answer.updated + " / " + answer.skipped;
      await this.load();
      toast().success(t("skills.synced"));
    } catch (err) {
      this.sync.error = err.message;
    } finally {
      this.sync.busy = false;
    }
  },

  setPlanField(path, event) {
    setPath(this.plan, path, event.target.value);
    this.savePlan(path, this.plan[path]);
  },

  setPlanBool(path, event) {
    this.plan[path] = event.target.checked;
    this.savePlan(path, event.target.checked);
  },

  async savePlan(key, value) {
    if (key === "autoPlanComplexityThreshold") {
      value = clampInt(value, 1, 10) ?? 6;
      this.plan[key] = value;
    }
    if (key === "autoPlanMaxTokens") {
      value = clampInt(value, 200, 4000) ?? 800;
      this.plan[key] = value;
    }
    if (key === "autoPlanTimeoutMs") {
      value = clampInt(value, 1000, 60000) ?? 15000;
      this.plan[key] = value;
    }
    await this.patch({ [key]: value });
  },

}, studioMethods, formatMethods));
