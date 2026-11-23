// The models half of a provider page: the registry list, test, disable, alias
// and custom models, and the import from an endpoint. Mixed into the page
// component.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { modelRow, sortModels } from "./provider-status.js";

export const modelMethods = {
  owner() {
    return this.entry ? (this.entry.alias || this.entry.id) : this.id;
  },

  // modelLabel is the form clients type and people read: the alias, then the
  // model. modelFull below is the stored target and never shown.
  modelLabel(id) {
    return this.alias() + "/" + id;
  },

  // showName hides a display name that only repeats the id.
  showName(model) {
    return model.name !== "" && model.name !== model.id;
  },

  // subtext is the line under a model's id: its display name when it has one,
  // otherwise the form a client types.
  subtext(model) {
    return this.showName(model) ? model.name : this.modelLabel(model.id);
  },

  modelFull(id) {
    return this.entry ? this.entry.id + "/" + id : this.id + "/" + id;
  },

  async loadModels() {
    this.models.error = "";
    this.models.loading = true;
    try {
      const custom = await api("/api/models/custom");
      const mine = (custom.models || []).filter((m) => m.providerAlias === this.owner());
      if (!this.entry) {
        this.models.items = mine.map((m) => ({ id: m.id, name: m.name || "", source: "custom", type: m.type }));
        this.models.disabled = [];
        return;
      }
      const [all, disabled, aliases] = await Promise.all([
        api("/api/models"),
        api("/api/models/disabled"),
        api("/api/models/alias"),
      ]);
      const names = [this.entry.id, this.entry.alias].filter(Boolean);
      this.models.items = (all.models || []).filter((m) => names.indexOf(m.provider) >= 0).map(modelRow);
      this.models.items = sortModels(this.models.items);
      const byTarget = {};
      for (const alias of Object.keys(aliases.aliases || {})) {
        byTarget[aliases.aliases[alias]] = alias;
      }
      this.models.byTarget = byTarget;
      const off = [];
      for (const name of names) {
        for (const id of ((disabled.disabled || {})[name] || [])) {
          if (off.indexOf(id) < 0) {
            off.push(id);
          }
        }
      }
      this.models.disabled = off;
    } catch (err) {
      this.models.error = err.message;
    } finally {
      this.models.loading = false;
    }
  },

  // setAlias points a model at an alias, or at none, in the table the page reads.
  // A model has one alias, so a new one replaces the old.
  setAlias(target, alias) {
    const next = Object.assign({}, this.models.byTarget);
    if (alias) {
      next[target] = alias;
    } else {
      delete next[target];
    }
    this.models.byTarget = next;
  },

  aliasFor(id) {
    return this.models.byTarget[this.modelFull(id)] || "";
  },

  testFor(id) {
    return this.models.test[id] || null;
  },

  async testModel(id) {
    this.models.test = Object.assign({}, this.models.test, { [id]: { busy: true } });
    try {
      const out = await api("/api/models/test", { method: "POST", body: { model: this.modelFull(id) } });
      this.models.test = Object.assign({}, this.models.test, {
        [id]: { ok: out.ok === true, error: out.error || "", latencyMs: out.latencyMs, busy: false },
      });
    } catch (err) {
      this.models.test = Object.assign({}, this.models.test, { [id]: { ok: false, error: err.message, busy: false } });
    }
  },

  disableModel(id) {
    return this.run("disable:" + id, async () => {
      try {
        await api("/api/models/disabled", { method: "POST", body: { providerAlias: this.owner(), ids: [id] } });
        // The list leaves a disabled model out and the disabled list gains it,
        // which is all the server changed; the registry is not read again.
        this.models.items = this.models.items.filter((m) => m.id !== id);
        if (this.models.disabled.indexOf(id) < 0) {
          this.models.disabled = this.models.disabled.concat([id]);
        }
      } catch (err) {
        toast().error(err.message);
      }
    });
  },

  async disableAll() {
    const yes = await ask({ title: t("models.disableAllTitle"), message: t("models.disableAllBody"), confirm: t("models.disableAll") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/models/disabled", {
        method: "POST",
        body: { providerAlias: this.owner(), ids: this.models.items.map((m) => m.id) },
      });
      await this.loadModels();
    } catch (err) {
      toast().error(err.message);
    }
  },

  async enableModel(id) {
    try {
      await api("/api/models/disabled?providerAlias=" + encodeURIComponent(this.owner()) + "&id=" + encodeURIComponent(id), { method: "DELETE" });
      await this.loadModels();
    } catch (err) {
      toast().error(err.message);
    }
  },

  async enableAll() {
    try {
      await api("/api/models/disabled?providerAlias=" + encodeURIComponent(this.owner()), { method: "DELETE" });
      await this.loadModels();
    } catch (err) {
      toast().error(err.message);
    }
  },

  openAlias(id) {
    this.aliasForm = { open: true, model: id, alias: this.aliasFor(id), error: "" };
    this.$refs.aliasDlg.showModal();
    this.$refs.aliasInput.focus();
  },

  closeAlias() {
    this.$refs.aliasDlg.close();
    this.aliasForm.open = false;
  },

  saveAlias() {
    return this.run("alias", async () => {
      this.aliasForm.error = "";
      try {
        const target = this.modelFull(this.aliasForm.model);
        await api("/api/models/alias", { method: "PUT", body: { model: target, alias: this.aliasForm.alias } });
        this.setAlias(target, this.aliasForm.alias);
        this.closeAlias();
      } catch (err) {
        this.aliasForm.error = err.message;
      }
    });
  },

  removeAlias(id) {
    const alias = this.aliasFor(id);
    if (!alias) {
      return Promise.resolve();
    }
    return this.run("alias:" + id, async () => {
      try {
        await api("/api/models/alias?alias=" + encodeURIComponent(alias), { method: "DELETE" });
        this.setAlias(this.modelFull(id), "");
      } catch (err) {
        toast().error(err.message);
      }
    });
  },

  openModelAdd() {
    this.modelAdd = { open: true, id: "", name: "", vision: false, reasoning: false, error: "", busy: false };
    this.$refs.modelDlg.showModal();
    this.$refs.modelId.focus();
  },

  closeModelAdd() {
    this.$refs.modelDlg.close();
    this.modelAdd.open = false;
  },

  toggleModelVision() {
    this.modelAdd.vision = !this.modelAdd.vision;
  },

  toggleModelReasoning() {
    this.modelAdd.reasoning = !this.modelAdd.reasoning;
  },

  async addCustomModel() {
    this.modelAdd.error = "";
    this.modelAdd.busy = true;
    try {
      await api("/api/models/custom", {
        method: "POST",
        body: {
          providerAlias: this.owner(), id: this.modelAdd.id, type: "llm", name: this.modelAdd.name,
          caps: { vision: this.modelAdd.vision, reasoning: this.modelAdd.reasoning },
        },
      });
      this.closeModelAdd();
      await this.loadModels();
    } catch (err) {
      this.modelAdd.error = err.message;
    } finally {
      this.modelAdd.busy = false;
    }
  },

  async removeCustom(row) {
    const yes = await ask({ title: t("models.removeTitle"), message: row.id, confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/models/custom?providerAlias=" + encodeURIComponent(this.owner()) + "&id=" + encodeURIComponent(row.id) + "&type=" + encodeURIComponent(row.type || "llm"), { method: "DELETE" });
      await this.loadModels();
    } catch (err) {
      toast().error(err.message);
    }
  },

  async importModels() {
    const conn = this.connections.find((c) => c.isActive !== false) || this.connections[0];
    if (!conn) {
      toast().error(t("models.importNeedsConnection"));
      return;
    }
    try {
      const out = await api("/api/providers/" + encodeURIComponent(conn.id) + "/models");
      const known = this.models.items.map((m) => m.id);
      let added = 0;
      let failed = 0;
      for (const model of out.models || []) {
        if (known.indexOf(model.id) >= 0) {
          continue;
        }
        try {
          await api("/api/models/custom", { method: "POST", body: { providerAlias: this.owner(), id: model.id, type: "llm", name: model.name || "" } });
          added++;
        } catch (err) {
          failed++;
        }
      }
      await this.loadModels();
      if (failed > 0) {
        toast().error(t("models.importPartial") + " " + added + " / " + failed);
      } else {
        toast().success(t("models.imported") + " " + added);
      }
    } catch (err) {
      toast().error(err.message);
    }
  },
};
