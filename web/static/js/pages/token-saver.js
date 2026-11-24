// Token savers: the switches and their knobs. Each control writes its own key
// and the answer replaces the view, so the page never drifts from the store.
import { api, patchSettings } from "/static/js/ui/api.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";

const levels = ["lite", "full", "ultra"];
const watermarkSkill = "watermarks-remover";

window.Alpine.data("tokenSaverPage", () => Object.assign({
  loading: true,
  error: "",
  settings: {},
  // skill is the watermark remover's own switch. PATCH /api/settings answers with
  // the settings view, which has no field for a skill, so it is read from the
  // skills list and re-read after each write.
  skill: { enabled: false, mode: "always" },

  init() {
    this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      this.settings = await api("/api/settings");
      await this.loadSkill();
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  async loadSkill() {
    try {
      const listing = await api("/api/skills");
      const found = (listing.skills || []).find((item) => item.id === watermarkSkill);
      this.skill = found ? { enabled: found.enabled === true, mode: found.mode || "always" } : this.skill;
    } catch (err) {
      this.error = err.message;
    }
  },

  // setSkillBool and setSkillEnum write the remover's own key, then read the
  // switch back so the page shows what was stored, not what was typed.
  async setSkillBool(event) {
    const box = event.target;
    if (!(await this.patchSkill({ [watermarkSkill + "Enabled"]: box.checked }))) {
      box.checked = !box.checked;
    }
  },

  async setSkillMode(event) {
    if (!(await this.patchSkill({ [watermarkSkill + "RoutingMode"]: event.target.value }))) {
      event.target.value = this.skill.mode;
    }
  },

  async patchSkill(body) {
    try {
      await patchSettings(body);
    } catch (err) {
      toast().error(err.message);
      return false;
    }
    await this.loadSkill();
    return true;
  },

  bool(key) {
    return this.settings[key] === true;
  },

  text(key) {
    const value = this.settings[key];
    return value === undefined ? "" : value;
  },

  levels() {
    return levels;
  },

  // displayLevel shows a stored Chinese level as ultra here: the wenyan prompts
  // are offered only in a Chinese locale, as the reference does.
  displayLevel(key) {
    const value = this.settings[key] || "full";
    return value.indexOf("wenyan") === 0 ? "ultra" : value;
  },

  levelName(value) {
    return t("saver.level." + value);
  },

  levelDesc(value) {
    return t("saver.levelDesc." + value);
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

  // restore puts a text control back to the stored value after a failed save.
  restore(key, event, fallback) {
    event.target.value = this.text(key) === "" ? fallback || "" : this.text(key);
  },

  async setBool(key, event) {
    const box = event.target;
    if (!(await this.patch({ [key]: box.checked }))) {
      box.checked = !box.checked;
    }
  },

  // setLevelFrom saves the chosen level and puts the select back on the stored
  // one when the save is refused.
  async setLevelFrom(key, event) {
    if (!(await this.patch({ [key]: event.target.value }))) {
      event.target.value = this.displayLevel(key);
    }
  },

  async setText(key, event) {
    if (!(await this.patch({ [key]: event.target.value }))) {
      this.restore(key, event);
    }
  },

  // The server's integer rule refuses the text a number input holds.
  async setNumber(key, event) {
    const n = parseInt(event.target.value, 10);
    if (isNaN(n)) {
      this.restore(key, event);
      return;
    }
    if (!(await this.patch({ [key]: n }))) {
      this.restore(key, event);
    }
  },

  async setEnum(key, event) {
    if (!(await this.patch({ [key]: event.target.value }))) {
      this.restore(key, event, "always");
    }
  },

}, formatMethods));
