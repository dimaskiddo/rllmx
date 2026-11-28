// The connections half of a provider page: the list and its order, testing, the
// add, edit and bulk dialogs. It is mixed into the page component, so it reads
// and writes the one state the page holds.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { checkAllowsSave, mergeConnection, psdBody, psdRows, renumber } from "./provider-status.js";

const nodeKeys = ["prefix", "apiType", "baseUrl", "nodeName"];
// keyless are the auth kinds with no key to paste: an OAuth login's token lives
// with the account, a pasted token is already stored, a lease is minted by the
// provider's desktop app.
const keyless = ["oauth", "access_token", "mcode-lease"];
// controlKey is the provider data field the OpenDesign quota reading needs; it
// has its own password input, so the generic field editor never lists it.
const controlKey = "opendesignControlKey";

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export const connectionMethods = {
  async reloadConnections() {
    try {
      const providers = await api("/api/providers");
      this.connections = (providers.connections || []).filter((c) => c.provider === this.id);
      this.pruneSelection();
      // A test or a request may have just locked a connection.
      this.startTimer();
    } catch (err) {
      toast().error(err.message);
    }
  },

  pruneSelection() {
    const ids = this.connections.map((c) => c.id);
    this.selected = this.selected.filter((id) => ids.indexOf(id) >= 0);
  },

  isSelected(id) {
    return this.selected.indexOf(id) >= 0;
  },

  toggleSelect(id) {
    this.selected = this.isSelected(id) ? this.selected.filter((item) => item !== id) : this.selected.concat([id]);
  },

  allSelected() {
    return this.connections.length > 0 && this.selected.length === this.connections.length;
  },

  selectAll() {
    this.selected = this.allSelected() ? [] : this.connections.map((c) => c.id);
  },

  toggle(conn) {
    return this.run("toggle:" + conn.id, async () => {
      try {
        const out = await api("/api/providers/" + encodeURIComponent(conn.id), { method: "PUT", body: { isActive: conn.isActive === false } });
        // The answer is the whole changed row, so the list is not read again.
        this.connections = mergeConnection(this.connections, out.connection);
        this.startTimer();
      } catch (err) {
        toast().error(err.message);
      }
    });
  },

  move(index, direction) {
    return this.run("move", () => this.moveRow(index, direction));
  },

  async moveRow(index, direction) {
    const moved = renumber(this.connections, index, direction);
    if (!moved) {
      return;
    }
    try {
      await Promise.all(moved.changes.map((change) => api("/api/providers/" + encodeURIComponent(change.id), {
        method: "PUT",
        body: { priority: change.priority },
      })));
      // The rows keep the numbers the server now holds, or the next move would
      // compare against stale ones and skip a write.
      moved.list.forEach((conn, position) => {
        conn.priority = position + 1;
      });
      this.connections = moved.list;
    } catch (err) {
      toast().error(err.message);
      await this.reloadConnections();
    }
  },

  test(conn) {
    return this.run("test:" + conn.id, () => this.testOne(conn));
  },

  async testOne(conn) {
    try {
      const out = await api("/api/providers/" + encodeURIComponent(conn.id) + "/test", { method: "POST", body: {} });
      if (out.valid) {
        toast().success(t("provider.testOk") + (out.latencyMs !== undefined ? " · " + out.latencyMs + " ms" : ""));
      } else {
        toast().error(out.error || t("provider.testFailed"));
      }
      await this.reloadConnections();
    } catch (err) {
      toast().error(err.message);
    }
  },

  async remove(conn) {
    const yes = await ask({ title: t("provider.deleteTitle"), message: t("provider.deleteBody"), confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/providers/" + encodeURIComponent(conn.id), { method: "DELETE" });
      await this.reloadConnections();
      toast().success(t("provider.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },

  async removeSelected() {
    const yes = await ask({ title: t("provider.bulkDeleteTitle"), message: t("provider.bulkDeleteBody"), confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    let failed = 0;
    for (const id of this.selected) {
      try {
        await api("/api/providers/" + encodeURIComponent(id), { method: "DELETE" });
      } catch (err) {
        failed++;
      }
    }
    if (failed) {
      toast().error(t("provider.bulkFailed"));
    }
    this.selected = [];
    await this.reloadConnections();
  },

  async testSelected() {
    if (this.testRun) {
      return;
    }
    this.testRun = { index: 0, total: this.connections.length, passed: 0, failed: 0, on: true, name: "" };
    for (let i = 0; i < this.connections.length; i++) {
      if (!this.testRun || !this.testRun.on) {
        break;
      }
      this.testRun.index = i + 1;
      this.testRun.name = this.title(this.connections[i]);
      try {
        const out = await api("/api/providers/" + encodeURIComponent(this.connections[i].id) + "/test", { method: "POST", body: {} });
        this.testRun[out.valid ? "passed" : "failed"]++;
      } catch (err) {
        this.testRun.failed++;
      }
      await sleep(1000);
    }
    const run = this.testRun;
    if (run) {
      run.on = false;
    }
    await this.reloadConnections();
    if (run && run.passed + run.failed > 0) {
      toast().info(t("provider.testDone") + " " + run.passed + " / " + run.failed);
    }
    this.testRun = null;
  },

  stopTesting() {
    if (this.testRun) {
      this.testRun.on = false;
    }
  },

  openAddKey() {
    this.add = {
      name: "", key: "", defaultModel: "", priority: "1", region: "", controlKey: "",
      psd: [], check: null, checking: false, busy: false, error: "",
    };
    this.$refs.addDlg.showModal();
    this.$refs.addName.focus();
  },

  closeAddKey() {
    this.$refs.addDlg.close();
  },

  // The dialogs below are closed by Escape as well as by their buttons, so a
  // typed secret is dropped on the dialog's own close event.
  forgetAdd() {
    this.add.key = "";
    this.add.controlKey = "";
    this.add.check = null;
  },

  forgetEdit() {
    if (this.edit) {
      this.edit.apiKey = "";
      this.edit.controlKey = "";
    }
  },

  setAddKey(event) {
    this.add.key = event.target.value;
    this.add.check = null;
  },

  setAddModel(event) {
    this.add.defaultModel = event.target.value;
    this.add.check = null;
  },

  addPsdRow() {
    this.add.psd = this.add.psd.concat([{ key: "", value: "", readonly: false }]);
  },

  setAddPsd(index, field, event) {
    this.add.psd = this.add.psd.map((row, i) => (i === index ? Object.assign({}, row, { [field]: event.target.value }) : row));
  },

  removeAddPsd(index) {
    this.add.psd = this.add.psd.filter((row, i) => i !== index);
  },

  addBody() {
    const body = { provider: this.id, name: this.add.name, apiKey: this.add.key };
    if (this.add.defaultModel !== "") {
      body.defaultModel = this.add.defaultModel;
    }
    const priority = Number(this.add.priority);
    if (isFinite(priority) && priority > 0) {
      body.priority = priority;
    }
    const psd = psdBody(this.add.psd);
    if (this.add.region !== "") {
      psd.region = this.add.region;
    }
    if (this.controlKeyField() && this.add.controlKey.trim() !== "") {
      psd[controlKey] = this.add.controlKey.trim();
    }
    if (Object.keys(psd).length > 0) {
      body.providerSpecificData = psd;
    }
    return body;
  },

  validateKey() {
    return api("/api/providers/validate", {
      method: "POST",
      body: { provider: this.id, apiKey: this.add.key, modelId: this.add.defaultModel },
    });
  },

  async checkAdd() {
    this.add.checking = true;
    this.add.check = null;
    this.add.error = "";
    try {
      this.add.check = await this.validateKey();
    } catch (err) {
      this.add.error = err.message;
    } finally {
      this.add.checking = false;
    }
  },

  async create(body, allowOverwrite) {
    const payload = Object.assign({}, body);
    if (allowOverwrite) {
      payload.allowOverwrite = true;
    }
    return api("/api/providers", { method: "POST", body: payload });
  },

  async saveAdd() {
    this.add.error = "";
    this.add.busy = true;
    try {
      const body = this.addBody();
      const check = await this.validateKey();
      this.add.check = check;
      if (!checkAllowsSave(check)) {
        this.add.error = check.error || t("provider.checkRequired");
        return;
      }
      try {
        await this.create(body, false);
      } catch (err) {
        if (err.status !== 409 || !err.data || err.data.code !== "PROVIDER_NAME_CONFLICT") {
          throw err;
        }
        const yes = await ask({ title: t("provider.overwriteTitle"), message: t("provider.overwriteBody"), confirm: t("provider.overwrite"), danger: true });
        if (!yes) {
          this.add.error = t("provider.conflict");
          return;
        }
        await this.create(body, true);
      }
      this.closeAddKey();
      await this.reloadConnections();
      toast().success(t("provider.saved"));
    } catch (err) {
      this.add.error = err.message;
    } finally {
      this.add.busy = false;
    }
  },

  openEdit(conn) {
    this.edit = {
      id: conn.id,
      authType: conn.authType || "",
      name: conn.name || "",
      priority: String(conn.priority || 1),
      defaultModel: conn.defaultModel || "",
      apiKey: "",
      controlKey: "",
      hasControlKey: !!(conn.providerSpecificData && conn.providerSpecificData[controlKey]),
      isActive: conn.isActive !== false,
      region: this.region(conn.providerSpecificData),
      psd: psdRows(conn.providerSpecificData, (this.node ? nodeKeys : []).concat([controlKey])),
      psdKeys: [],
      check: null,
      checking: false,
      error: "",
      busy: false,
    };
    this.edit.psdKeys = this.edit.psd.filter((row) => !row.readonly).map((row) => row.key);
    this.$refs.editDlg.showModal();
    this.$refs.editName.focus();
  },

  // editUsesKey is true for a connection whose credential is a key. An OAuth
  // login, a pasted token and a lease have none to type.
  editUsesKey() {
    return !!this.edit && keyless.indexOf(this.edit.authType) < 0;
  },

  // A typed key is a new credential, so the result of the previous one no
  // longer describes it.
  setEditKey(event) {
    this.setField("edit.apiKey", event);
    this.edit.check = null;
  },

  validateEdit() {
    return api("/api/providers/validate", {
      method: "POST",
      body: { provider: this.id, apiKey: this.edit.apiKey, modelId: this.edit.defaultModel },
    });
  },

  async checkEdit() {
    this.edit.checking = true;
    this.edit.check = null;
    this.edit.error = "";
    try {
      this.edit.check = await this.validateEdit();
    } catch (err) {
      this.edit.error = err.message;
    } finally {
      this.edit.checking = false;
    }
  },

  closeEdit() {
    this.$refs.editDlg.close();
    this.edit = null;
  },

  toggleEditActive() {
    this.edit.isActive = !this.edit.isActive;
  },

  editPsdRow() {
    this.edit.psd = this.edit.psd.concat([{ key: "", value: "", readonly: false }]);
  },

  setEditPsd(index, field, event) {
    this.edit.psd = this.edit.psd.map((row, i) => (i === index ? Object.assign({}, row, { [field]: event.target.value }) : row));
  },

  removeEditPsd(index) {
    this.edit.psd = this.edit.psd.filter((row, i) => i !== index);
  },

  editBody() {
    const body = {
      name: this.edit.name,
      defaultModel: this.edit.defaultModel === "" ? null : this.edit.defaultModel,
      priority: Number(this.edit.priority) || 1,
      isActive: this.edit.isActive,
      providerSpecificData: psdBody(this.edit.psd, this.edit.psdKeys),
    };
    if (this.editUsesKey() && this.edit.apiKey !== "") {
      body.apiKey = this.edit.apiKey;
    }
    if (this.controlKeyField() && this.edit.controlKey.trim() !== "") {
      body.providerSpecificData[controlKey] = this.edit.controlKey.trim();
    }
    if (this.entry && this.entry.regions && this.entry.regions.length > 0) {
      body.providerSpecificData.region = this.edit.region === "" ? null : this.edit.region;
    }
    return body;
  },

  async saveEdit() {
    this.edit.error = "";
    this.edit.busy = true;
    try {
      if (this.editUsesKey() && this.edit.apiKey !== "") {
        const check = await this.validateEdit();
        this.edit.check = check;
        if (!checkAllowsSave(check)) {
          this.edit.error = check.error || t("provider.checkRequired");
          return;
        }
      }
      await api("/api/providers/" + encodeURIComponent(this.edit.id), { method: "PUT", body: this.editBody() });
      this.closeEdit();
      await this.reloadConnections();
      toast().success(t("provider.saved"));
    } catch (err) {
      this.edit.error = err.message;
    } finally {
      this.edit.busy = false;
    }
  },

  openBulk() {
    this.bulk = { text: "", results: null, busy: false, error: "" };
    this.$refs.bulkDlg.showModal();
  },

  closeBulk() {
    this.$refs.bulkDlg.close();
  },

  // forgetBulk drops the pasted keys whichever way the dialog went away.
  forgetBulk() {
    this.bulk.text = "";
  },

  async runBulk() {
    this.bulk.error = "";
    this.bulk.busy = true;
    try {
      const out = await api("/api/providers/bulk", { method: "POST", body: { provider: this.id, entries: this.bulk.text } });
      this.bulk.results = out.results || [];
      await this.reloadConnections();
    } catch (err) {
      this.bulk.error = err.message;
    } finally {
      this.bulk.busy = false;
    }
  },
};
