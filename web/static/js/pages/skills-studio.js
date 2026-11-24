// The Studio: one dialog that creates or edits a custom skill. It writes the
// manifest fields the server accepts and none it refuses, so a saved skill
// declares data and never a process.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { setPath } from "/static/js/ui/fields.js";

const studioHooks = ["system-prompt", "pre-route", "pre-request"];

function emptyStudio() {
  return { mode: "create", id: "", name: "", description: "", hook: "system-prompt", prompt: "", rules: [], routing: "smart", triggers: "", keywords: "", error: "", busy: false };
}

// terms splits a comma-separated field into the list the manifest holds.
function terms(value) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function emptyRule() {
  return { modelRegex: "", messageRegex: "", injectPrompt: "", rerouteModel: "" };
}

function categoryOf(hook) {
  return hook === "system-prompt" ? "prompt-injection" : "middleware";
}

export const studioMethods = {
  studioHooks() {
    return studioHooks;
  },

  openCreate() {
    this.studio = emptyStudio();
    this.$refs.studio.showModal();
  },

  async openEdit(skill) {
    this.studio = emptyStudio();
    const studio = this.studio;
    studio.mode = "edit";
    studio.id = skill.id;
    studio.name = skill.name;
    studio.description = skill.description || "";
    studio.hook = studioHooks.indexOf(skill.hook) >= 0 ? skill.hook : "system-prompt";
    studio.routing = skill.routing_mode === "always" ? "always" : "smart";
    studio.triggers = (skill.triggers || []).join(", ");
    studio.keywords = (skill.keywords || []).join(", ");
    this.$refs.studio.showModal();
    try {
      const item = await api("/api/skills/" + encodeURIComponent(skill.id));
      if (studio !== this.studio) {
        return;
      }
      this.studio.prompt = item.promptTemplate || "";
      const raw = item.rules;
      const rows = raw && Array.isArray(raw.rules) ? raw.rules : [];
      this.studio.rules = rows.map((rule) => ({
        modelRegex: (rule.when && rule.when.modelRegex) || "",
        messageRegex: (rule.when && rule.when.messageRegex) || "",
        injectPrompt: (rule.then && rule.then.injectPrompt) || "",
        rerouteModel: (rule.then && rule.then.rerouteModel) || "",
      }));
    } catch (err) {
      if (studio === this.studio) {
        studio.error = err.message;
      }
    }
  },

  closeStudio() {
    this.studio = null;
    this.$refs.studio.close();
  },

  setStudioField(path, event) {
    setPath(this.studio, path, event.target.value);
  },

  setHook(value) {
    this.studio.hook = value;
  },

  setRouting(event) {
    this.studio.routing = event.target.value;
  },

  studioRules() {
    return this.studio ? this.studio.rules : [];
  },

  ruleHook() {
    return this.studio ? this.studio.hook : "system-prompt";
  },

  addRule() {
    this.studio.rules.push(emptyRule());
  },

  removeRule(index) {
    this.studio.rules.splice(index, 1);
  },

  setRule(index, path, event) {
    setPath(this.studio.rules[index], path, event.target.value);
  },

  draftBody() {
    const s = this.studio;
    const body = {
      name: s.name.trim(),
      description: s.description.trim(),
      hook: s.hook,
      category: categoryOf(s.hook),
      default_enabled: true,
    };
    if (s.hook === "system-prompt") {
      body.prompt_template = s.prompt;
      body.routable = true;
      body.routing_mode = s.routing;
      body.triggers = terms(s.triggers);
      body.keywords = terms(s.keywords);
    } else if (s.rules.length > 0) {
      body.rules = {
        rules: s.rules.map((rule) => ({
          stage: s.hook,
          when: {
            modelRegex: rule.modelRegex.trim(),
            messageRegex: rule.messageRegex.trim(),
          },
          then: {
            injectPrompt: rule.injectPrompt,
            rerouteModel: rule.rerouteModel.trim(),
          },
        })),
      };
    }
    return body;
  },

  async saveStudio() {
    const s = this.studio;
    s.error = "";
    if (s.name.trim() === "") {
      s.error = t("skills.studioNeedsName");
      return;
    }
    s.busy = true;
    try {
      const body = this.draftBody();
      if (s.mode === "edit") {
        await api("/api/skills/" + encodeURIComponent(s.id), { method: "PUT", body: body });
      } else {
        await api("/api/skills", { method: "POST", body: body });
      }
      this.closeStudio();
      await this.load();
      toast().success(t("skills.saved"));
    } catch (err) {
      s.error = err.message;
    } finally {
      if (s) {
        s.busy = false;
      }
    }
  },

  async removeSkill(skill) {
    const yes = await ask({ title: t("skills.deleteTitle"), message: t("skills.deleteBody") + " " + skill.name, danger: true, confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/skills/" + encodeURIComponent(skill.id), { method: "DELETE" });
      await this.load();
      toast().success(t("skills.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },
};
