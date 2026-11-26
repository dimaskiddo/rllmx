// The settings half of a provider page: the round-robin strategy, the custom
// endpoint and the header overrides. Mixed into the page component.
import { api, patchSettings } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { checkAllowsSave } from "/static/js/pages/provider-status.js";

// strategyEntry reads a provider's stored strategy as the object form. Older
// rows hold the bare word, which the selector reads as its fallbackStrategy.
function strategyEntry(stored) {
  if (typeof stored === "string" && stored !== "") {
    return { fallbackStrategy: stored };
  }
  return stored && typeof stored === "object" ? Object.assign({}, stored) : {};
}

export const settingsMethods = {
  applyStrategy() {
    const raw = this.settings.providerStrategies || {};
    const own = strategyEntry(raw[this.id]);
    this.strategy.roundRobin = own.fallbackStrategy === "round-robin";
    const limit = own.stickyRoundRobinLimit || this.settings.stickyRoundRobinLimit || 3;
    this.strategy.sticky = String(limit);
  },

  toggleRoundRobin() {
    this.strategy.roundRobin = !this.strategy.roundRobin;
    this.saveStrategy();
  },

  setSticky(event) {
    this.strategy.sticky = event.target.value;
    if (this.strategy.roundRobin) {
      this.saveStrategy();
    }
  },

  async saveStrategy() {
    try {
      const fresh = await api("/api/settings");
      const current = Object.assign({}, fresh.providerStrategies || {});
      const own = strategyEntry(current[this.id]);
      if (this.strategy.roundRobin) {
        own.fallbackStrategy = "round-robin";
        const limit = Number(this.strategy.sticky);
        if (this.strategy.sticky !== "" && isFinite(limit) && limit > 0) {
          own.stickyRoundRobinLimit = limit;
        } else {
          delete own.stickyRoundRobinLimit;
        }
      } else {
        delete own.fallbackStrategy;
        delete own.stickyRoundRobinLimit;
      }
      if (Object.keys(own).length === 0) {
        delete current[this.id];
      } else {
        current[this.id] = own;
      }
      await patchSettings({ providerStrategies: current });
      this.settings.providerStrategies = current;
      toast().success(t("provider.strategySaved"));
    } catch (err) {
      this.applyStrategy();
      toast().error(err.message);
    }
  },

  openNode() {
    this.nodeForm = {
      name: this.node.name, prefix: this.node.prefix || "", apiType: this.node.apiType || "chat",
      baseUrl: this.node.baseUrl || "", apiKey: "", modelId: "", check: null, checking: false, error: "", busy: false,
    };
    this.$refs.nodeDlg.showModal();
  },

  closeNode() {
    this.$refs.nodeDlg.close();
  },

  // The check key is a typed secret, so it goes whichever way the dialog did.
  forgetNode() {
    if (this.nodeForm) {
      this.nodeForm.apiKey = "";
      this.nodeForm.check = null;
    }
  },

  // The key here is given to the check only; the node keeps the credential it
  // already has.
  setNodeChecked(field, event) {
    this.nodeForm[field] = event.target.value;
    this.nodeForm.check = null;
  },

  async checkNodeEdit() {
    this.nodeForm.checking = true;
    this.nodeForm.check = null;
    this.nodeForm.error = "";
    try {
      this.nodeForm.check = await api("/api/provider-nodes/validate", {
        method: "POST",
        body: { baseUrl: this.nodeForm.baseUrl, apiKey: this.nodeForm.apiKey, type: this.node.type, modelId: this.nodeForm.modelId },
      });
    } catch (err) {
      this.nodeForm.error = err.message;
    } finally {
      this.nodeForm.checking = false;
    }
  },

  async saveNode() {
    this.nodeForm.error = "";
    this.nodeForm.busy = true;
    try {
      if (this.nodeForm.apiKey !== "") {
        await this.checkNodeEdit();
        if (!checkAllowsSave(this.nodeForm.check)) {
          if (this.nodeForm.error === "") {
            this.nodeForm.error = t("provider.checkRequired");
          }
          return;
        }
      }
      const body = { name: this.nodeForm.name, prefix: this.nodeForm.prefix, baseUrl: this.nodeForm.baseUrl, type: this.node.type };
      if (this.node.type === "openai-compatible") {
        body.apiType = this.nodeForm.apiType;
      }
      await api("/api/provider-nodes/" + encodeURIComponent(this.node.id), { method: "PUT", body: body });
      this.closeNode();
      await this.load();
      toast().success(t("provider.nodeSaved"));
    } catch (err) {
      this.nodeForm.error = err.message;
    } finally {
      this.nodeForm.busy = false;
    }
  },

  async deleteNode() {
    const yes = await ask({ title: t("provider.nodeDeleteTitle"), message: t("provider.nodeDeleteBody"), confirm: t("common.delete"), danger: true });
    if (!yes) {
      return;
    }
    try {
      await api("/api/provider-nodes/" + encodeURIComponent(this.node.id), { method: "DELETE" });
      location.assign("/dashboard/providers");
    } catch (err) {
      toast().error(err.message);
    }
  },

  async loadOverrides() {
    this.overrides.error = "";
    try {
      const out = await api("/api/providers/" + encodeURIComponent(this.id) + "/overrides");
      const builtin = out.builtinHeaders || {};
      const saved = out.headers || {};
      const names = Object.keys(builtin);
      for (const name of Object.keys(saved)) {
        if (names.indexOf(name) < 0) {
          names.push(name);
        }
      }
      this.overrides.rows = names.sort().map((name) => ({
        name: name,
        value: name in saved ? saved[name] : builtin[name],
        builtin: name in builtin ? builtin[name] : "",
      }));
    } catch (err) {
      this.overrides.error = err.message;
    }
  },

  changedHeader(row) {
    return row.value !== (row.builtin || "");
  },

  async resetOverrides() {
    this.overrides.error = "";
    this.overrides.busy = true;
    try {
      await api("/api/providers/" + encodeURIComponent(this.id) + "/overrides", { method: "PUT", body: { headers: {} } });
      await this.loadOverrides();
      toast().success(t("overrides.reset"));
    } catch (err) {
      this.overrides.error = err.message;
    } finally {
      this.overrides.busy = false;
    }
  },

  addHeaderRow() {
    this.overrides.rows = this.overrides.rows.concat([{ name: "", value: "" }]);
  },

  setHeader(index, field, event) {
    this.overrides.rows = this.overrides.rows.map((row, i) => (i === index ? Object.assign({}, row, { [field]: event.target.value }) : row));
  },

  removeHeaderRow(index) {
    this.overrides.rows = this.overrides.rows.filter((row, i) => i !== index);
  },

  async saveOverrides() {
    this.overrides.error = "";
    this.overrides.busy = true;
    const headers = {};
    for (const row of this.overrides.rows) {
      if (row.name !== "" && this.changedHeader(row)) {
        headers[row.name] = row.value;
      }
    }
    try {
      await api("/api/providers/" + encodeURIComponent(this.id) + "/overrides", { method: "PUT", body: { headers: headers } });
      await this.loadOverrides();
      toast().success(t("overrides.saved"));
    } catch (err) {
      this.overrides.error = err.message;
    } finally {
      this.overrides.busy = false;
    }
  },
};
