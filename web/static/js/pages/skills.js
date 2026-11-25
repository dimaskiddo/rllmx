// Skills: the installed list, each skill's switch and config fields, the
// catalog sync and the auto-plan orchestration.
import { api, patchSettings } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
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
  globalOn: true,
  builtinOn: true,
  customOn: true,
  eccGroupOn: true,
  eccOn: false,
  eccConfidence: 0.35,
  eccMax: 1,
  combos: [],
  search: "",
  hook: "all",
  sync: { force: false, busy: false, result: "", error: "" },
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
      this.globalOn = settings.globalSkillsEnabled !== false;
      this.builtinOn = settings.builtinSkillsEnabled !== false;
      this.customOn = settings.customSkillsEnabled !== false;
      this.eccGroupOn = settings.eccSkillsEnabled !== false;
      this.eccOn = settings.ecc_auto_skill_routerEnabled === true;
      this.eccConfidence = settings.ecc_auto_skill_routerConfidence || 0.35;
      this.eccMax = settings.ecc_auto_skill_routerMaxSkills || 1;
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

  // visible lists what this page owns. The watermark remover has its own card on
  // the Token Saver page, so it is not offered twice.
  visible() {
    const q = this.search.trim().toLowerCase();
    return this.skills.filter((skill) => {
      if (skill.id === "watermarks-remover") {
        return false;
      }
      if (this.hook !== "all" && skill.hook !== this.hook) {
        return false;
      }
      if (!q) {
        return true;
      }
      return (skill.name + " " + skill.id + " " + (skill.description || "")).toLowerCase().indexOf(q) >= 0;
    });
  },

  // cards splits the visible skills into the three panels the page shows: the
  // built-in prompts and rules behind the master switch, the imported catalog
  // behind its own router switch, and the operator's own skills. Each panel
  // carries the switch it writes; null means the panel has none.
  cards() {
    const buckets = { prompt: [], rules: [], ecc: [], custom: [] };
    for (const skill of this.visible()) {
      let bucket = buckets.prompt;
      if (skill.source === "custom") {
        bucket = buckets.custom;
      } else if (skill.source === "ecc") {
        bucket = buckets.ecc;
      } else if (skill.hook === "pre-route" || skill.hook === "pre-request") {
        bucket = buckets.rules;
      }
      bucket.push(skill);
    }
    const cards = [
      {
        key: "builtin",
        title: t("skills.builtinTitle"),
        on: this.builtinOn,
        off: this.builtinOn !== true,
        subgroups: [
          { key: "prompt", title: "", rows: buckets.prompt },
          { key: "rules", title: t("skills.groupRules"), rows: buckets.rules },
        ].filter((subgroup) => subgroup.rows.length > 0),
      },
    ];
    if (buckets.custom.length > 0) {
      cards.push({
        key: "custom",
        title: t("skills.groupCustom"),
        on: this.customOn,
        off: this.customOn !== true,
        subgroups: [{ key: "custom", title: "", rows: buckets.custom }],
      });
    }
    cards.push({
      key: "catalog",
      title: t("skills.catalogTitle"),
      on: this.eccGroupOn,
      off: this.eccGroupOn !== true,
      subgroups: [{ key: "ecc", title: "", rows: buckets.ecc }],
    });
    return cards;
  },

  // toggleCard is a kill switch: it writes its group key and never touches a
  // row's own switch, so turning a group back on restores what the rows say.
  async toggleCard(card, event) {
    const box = event.target;
    const keys = {
      builtin: "builtinSkillsEnabled",
      custom: "customSkillsEnabled",
      catalog: "eccSkillsEnabled",
    };
    const key = keys[card.key];
    if (!key || !(await this.patch({ [key]: box.checked }))) {
      box.checked = !box.checked;
      return;
    }
    if (card.key === "builtin") {
      this.builtinOn = box.checked;
    } else if (card.key === "custom") {
      this.customOn = box.checked;
    } else {
      this.eccGroupOn = box.checked;
    }
  },

  // setRouter starts or stops the catalog's own router; it enables no skill.
  async setRouter(event) {
    const box = event.target;
    if (await this.patch({ ecc_auto_skill_routerEnabled: box.checked })) {
      this.eccOn = box.checked;
    } else {
      box.checked = !box.checked;
    }
  },

  // groupOffSkill reports the group a skill belongs to being switched off.
  groupOffSkill(skill) {
    if (skill.source === "custom") {
      return this.customOn !== true;
    }
    if (skill.source === "ecc") {
      return this.eccGroupOn !== true;
    }
    return this.builtinOn !== true;
  },

  // rowOn gates a row's controls: the page-wide master, and the card's group.
  rowOn(skill) {
    return this.globalOn === true && !this.groupOffSkill(skill);
  },

  eccPercent() {
    return Math.round(this.eccConfidence * 100) + "%";
  },

  async setEccConfidence(event) {
    const value = Math.round(parseFloat(event.target.value) * 20) / 20;
    if (isNaN(value)) {
      return;
    }
    const clamped = Math.min(0.9, Math.max(0.1, value));
    this.eccConfidence = clamped;
    event.target.value = clamped;
    await this.saveOrReload({ ecc_auto_skill_routerConfidence: clamped });
  },

  async setEccMax(event) {
    const value = clampInt(event.target.value, 1, 3) ?? 1;
    this.eccMax = value;
    event.target.value = value;
    await this.saveOrReload({ ecc_auto_skill_routerMaxSkills: value });
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
      await patchSettings(body);
      return true;
    } catch (err) {
      toast().error(err.message);
      return false;
    }
  },

  // saveOrReload saves a setting and, when the server refuses it, reads the page
  // again so no control keeps showing a value that was not stored.
  async saveOrReload(body) {
    if (!(await this.patch(body))) {
      await this.load();
    }
  },

  async toggle(skill, event) {
    const box = event.target;
    if (skill.id === "rea-allow" && box.checked) {
      const yes = await ask({ title: t("skills.reaTitle"), message: t("skills.reaBody"), danger: true, confirm: t("skills.reaConfirm") });
      if (!yes) {
        box.checked = false;
        return;
      }
    }
    if (await this.patch({ [skill.id + "Enabled"]: box.checked })) {
      skill.enabled = box.checked;
    } else {
      box.checked = !box.checked;
    }
  },

  setMode(skill, event) {
    return this.saveOrReload({ [skill.id + "RoutingMode"]: event.target.value });
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

  setSyncForce(event) {
    this.sync.force = event.target.checked;
  },

  async runSync() {
    this.sync.error = "";
    this.sync.result = "";
    this.sync.busy = true;
    try {
      const answer = await api("/api/skills/sync", { method: "POST", body: { force: this.sync.force } });
      this.sync.result = t("skills.syncedCount").replace("{n}", answer.total);
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
    await this.saveOrReload({ [key]: value });
  },

}, studioMethods, formatMethods));
