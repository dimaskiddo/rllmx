// The sign-in page. Its one decision worth keeping explicit: it bounces to the
// dashboard only when a session exists. With requireLogin off a session is
// still needed for keys and logins, so bouncing there would loop.
import Alpine from "/static/lib/alpine.csp.js";
import "./ui/directives.js";
import { api, nextPath } from "./ui/api.js";
import { load, locale, t } from "./ui/i18n.js";

Alpine.data("loginPage", () => ({
  ready: false,
  password: "",
  error: "",
  busy: false,
  locked: 0,
  timer: null,
  target: "/dashboard",
  form: true,
  sso: { oidc: false, saml: false, oidcLabel: "", samlLabel: "" },
  hint: { defaultPassword: false },
  init() {
    this.target = nextPath(location.search);
    this.check();
  },
  destroy() {
    this.stopClock();
  },
  async check() {
    let status = null;
    try {
      const controller = new AbortController();
      const abort = setTimeout(controller.abort.bind(controller), 5000);
      const res = await fetch("/api/auth/status", { credentials: "same-origin", signal: controller.signal });
      clearTimeout(abort);
      status = res.ok ? await res.json() : null;
    } catch (err) {
      // A slow or failed check leaves the password form: it is the login a bad
      // identity-provider answer cannot break.
    }
    if (status && status.authenticated === true) {
      location.assign(this.target);
      return;
    }
    if (status) {
      this.form = status.password !== false;
      this.sso = {
        oidc: status.oidc === true,
        saml: status.saml === true,
        oidcLabel: status.oidcLoginLabel || t("login.oidc"),
        samlLabel: status.samlLoginLabel || t("login.saml"),
      };
      this.hint.defaultPassword = status.defaultPassword === true;
    }
    const code = new URLSearchParams(location.search).get("error");
    if (code) {
      const text = t("sso." + code);
      // t() answers the key itself for a code it does not know, and the code
      // came from the address bar: show the generic message instead of it.
      this.error = text === "sso." + code ? t("sso.sso_idp_error") : text;
    }
    this.ready = true;
  },
  setPassword(event) {
    this.password = event.target.value;
  },
  async submit() {
    if (this.busy || this.locked > 0) {
      return;
    }
    this.busy = true;
    this.error = "";
    try {
      await api("/api/auth/login", { method: "POST", body: { password: this.password } });
      location.assign(this.target);
    } catch (err) {
      if (err.status === 429) {
        this.startClock((err.data && err.data.retryAfter) || 30);
      }
      if (err.data && err.data.mustChangePassword) {
        this.error = t("login.changeFromMachine");
      } else {
        this.error = err.message;
      }
    } finally {
      this.busy = false;
    }
  },
  startClock(seconds) {
    this.stopClock();
    this.locked = seconds;
    this.timer = setInterval(() => {
      this.locked = this.locked > 0 ? this.locked - 1 : 0;
      if (this.locked === 0) {
        this.stopClock();
      }
    }, 1000);
  },
  stopClock() {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  },
}));

await load();
document.documentElement.lang = locale();
Alpine.start();
