// The login half of a provider page: the code and device flows, the token and
// import forms, and the timers that stop the moment the page or the dialog
// goes away. It is mixed into the page component so both share one state.
import { api } from "/static/js/ui/api.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";

export const loginMethods = {
  // Logins.

  // clearPoll ends the login's polling for good. Every starter and poller holds
  // the loginRun it began under and stops when it no longer matches, so a reply
  // that lands after the dialog closed, reopened or the page left does nothing.
  clearPoll() {
    this.loginRun++;
    if (this.pollTimer) {
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    }
  },

  resetLogin(kind) {
    this.clearPoll();
    this.login = {
      open: true, kind: kind, busy: false, error: "", state: "", authUrl: "", paste: false, input: "",
      userCode: "", verificationUri: "", variant: "", mode: "",
      app: { baseUrl: "", clientId: "", clientSecret: "" },
      token: "", baseUrl: "", email: "", fields: {}, bulk: "", results: null,
    };
  },

  openLogin(kind) {
    this.resetLogin(kind);
    this.$refs.loginDlg.showModal();
    if (kind !== "oauth") {
      return;
    }
    const offer = this.loginOffer();
    if (offer.flow === "device") {
      this.startDevice();
      return;
    }
    if (offer.variants && offer.variants.length > 0) {
      this.login.variant = offer.variants[0];
      return;
    }
    if (!offer.appFromAccount) {
      this.startCode();
    }
  },

  closeLogin() {
    this.clearPoll();
    this.$refs.loginDlg.close();
    this.login.open = false;
  },

  // The dialog's own close event fires for Escape too, so polling stops
  // however the dialog went away.
  loginClosed() {
    this.clearPoll();
    this.login.open = false;
    this.login.token = "";
    this.login.input = "";
    this.login.app.clientSecret = "";
  },

  setLoginApp(field, event) {
    this.login.app[field] = event.target.value;
  },

  setLoginField(name, event) {
    this.login.fields[name] = event.target.value;
  },

  openTab(url) {
    if (url) {
      window.open(url, "_blank", "noopener");
    }
  },

  async startCode() {
    const run = this.loginRun;
    this.login.error = "";
    this.login.busy = true;
    try {
      let out;
      if (this.loginOffer().appFromAccount) {
        out = await api("/api/oauth/" + encodeURIComponent(this.id) + "/authorize", {
          method: "POST",
          body: { baseUrl: this.login.app.baseUrl, clientId: this.login.app.clientId, clientSecret: this.login.app.clientSecret },
        });
      } else {
        const query = this.login.variant ? "?variant=" + encodeURIComponent(this.login.variant) : "";
        out = await api("/api/oauth/" + encodeURIComponent(this.id) + "/authorize" + query);
      }
      if (run !== this.loginRun) {
        return;
      }
      this.login.state = out.state;
      this.login.authUrl = out.authUrl;
      this.login.paste = out.paste === true;
      this.login.mode = "code";
      this.openTab(out.authUrl);
      this.pollStatus(Date.now() + 600000, run);
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  async pollStatus(deadline, run) {
    if (run !== this.loginRun || !this.login.open || !this.login.state) {
      return;
    }
    if (Date.now() > deadline) {
      this.login.error = t("provider.loginExpired");
      return;
    }
    try {
      const out = await api("/api/oauth/status?state=" + encodeURIComponent(this.login.state));
      if (run !== this.loginRun) {
        return;
      }
      if (out.status === "done") {
        await this.finishLogin();
        return;
      }
      if (out.status === "error") {
        this.login.error = out.error || t("provider.loginFailed");
        return;
      }
      if (out.status === "unknown") {
        this.login.error = t("provider.loginExpired");
        return;
      }
    } catch (err) {
      if (run === this.loginRun) {
        this.login.error = err.message;
      }
      return;
    }
    this.pollTimer = setTimeout(() => this.pollStatus(deadline, run), 2000);
  },

  async pasteLogin() {
    this.login.error = "";
    this.login.busy = true;
    try {
      await api("/api/oauth/exchange", { method: "POST", body: { state: this.login.state, input: this.login.input } });
      await this.finishLogin();
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  async startDevice() {
    const run = this.loginRun;
    this.login.error = "";
    this.login.busy = true;
    try {
      const out = await api("/api/oauth/" + encodeURIComponent(this.id) + "/device-code");
      if (run !== this.loginRun) {
        return;
      }
      this.login.state = out.state;
      this.login.userCode = out.userCode || "";
      this.login.verificationUri = out.verificationUri || "";
      this.login.mode = "device";
      this.openTab(out.verificationUri);
      const interval = Math.max(2000, (out.interval || 5) * 1000);
      const deadline = Date.now() + (out.expiresIn || 120) * 1000;
      this.pollDevice(interval, deadline, run);
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  async pollDevice(interval, deadline, run) {
    if (run !== this.loginRun || !this.login.open || !this.login.state) {
      return;
    }
    if (Date.now() > deadline) {
      this.login.error = t("provider.loginExpired");
      return;
    }
    let wait = interval;
    try {
      const out = await api("/api/oauth/poll", { method: "POST", body: { state: this.login.state } });
      if (run !== this.loginRun) {
        return;
      }
      if (out.status === "done") {
        await this.finishLogin();
        return;
      }
      if (out.status === "error") {
        this.login.error = out.error || t("provider.loginFailed");
        return;
      }
      if (out.status === "unknown") {
        this.login.error = t("provider.loginExpired");
        return;
      }
      if (out.interval) {
        wait = Math.min(Math.max(wait, out.interval * 1000), 30000);
      }
    } catch (err) {
      if (run === this.loginRun) {
        this.login.error = err.message;
      }
      return;
    }
    this.pollTimer = setTimeout(() => this.pollDevice(wait, deadline, run), wait);
  },

  async patLogin() {
    this.login.error = "";
    this.login.busy = true;
    try {
      const body = { token: this.login.token };
      if (this.login.baseUrl !== "") {
        body.baseUrl = this.login.baseUrl;
      }
      await api("/api/oauth/" + encodeURIComponent(this.id) + "/pat", { method: "POST", body: body });
      await this.finishLogin();
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  async importLogin() {
    this.login.error = "";
    this.login.busy = true;
    try {
      const body = { accessToken: this.login.token };
      for (const field of (this.loginOffer() ? this.loginOffer().fields || [] : [])) {
        body[field] = this.login.fields[field] || "";
      }
      if (this.login.email !== "") {
        body.email = this.login.email;
      }
      await api("/api/oauth/" + encodeURIComponent(this.id) + "/import", { method: "POST", body: body });
      await this.finishLogin();
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  async bulkImport() {
    this.login.error = "";
    this.login.busy = true;
    try {
      let parsed;
      try {
        parsed = JSON.parse(this.login.bulk);
      } catch (err) {
        this.login.error = t("provider.bulkInvalid");
        return;
      }
      const out = await api("/api/oauth/" + encodeURIComponent(this.id) + "/bulk-import", { method: "POST", body: parsed });
      this.login.results = out.results || [];
      await this.reloadConnections();
    } catch (err) {
      this.login.error = err.message;
    } finally {
      this.login.busy = false;
    }
  },

  // finishLogin reports its own failure: it ends the polling first, so the
  // caller's catch would no longer be allowed to show an error.
  async finishLogin() {
    this.closeLogin();
    toast().success(t("provider.loginDone"));
    try {
      await this.reloadConnections();
    } catch (err) {
      toast().error(err.message);
    }
  },
};
