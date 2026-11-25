// The sign-in page. Its one decision worth keeping explicit: it bounces to the
// dashboard only when a session exists. With requireLogin off a session is
// still needed for keys and logins, so bouncing there would loop.
import Alpine from "/static/lib/alpine.csp.js";
import "./ui/directives.js";
import { api, nextPath } from "./ui/api.js";
import { load, locale, t } from "./ui/i18n.js";

Alpine.data("loginPage", () => ({
  ready: false,
  username: "",
  password: "",
  error: "",
  busy: false,
  locked: 0,
  timer: null,
  target: "/dashboard",
  form: true,
  sso: { oidc: false, saml: false, oidcLabel: "", samlLabel: "" },
  hint: { defaultPassword: false },
  captcha: null,
  captchaToken: "",
  captchaWidget: null,
  captchaFailed: false,
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
      if (status.captcha) {
        this.setupCaptcha(status.captcha);
      }
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
  setUsername(event) {
    this.username = event.target.value;
  },
  setPassword(event) {
    this.password = event.target.value;
  },
  // The server names the provider and its loader, so no file here carries
  // another origin's address. The loader calls the one hook it was given once
  // its own interface exists; both providers render and reset the same way.
  setupCaptcha(settings) {
    this.captcha = settings;
    window.rllmCaptchaReady = () => this.renderCaptcha();
    const script = document.createElement("script");
    script.src = settings.script;
    script.async = true;
    script.defer = true;
    script.addEventListener("error", () => {
      this.captchaFailed = true;
    });
    document.head.appendChild(script);
  },
  renderCaptcha() {
    const api = window.turnstile || window.grecaptcha;
    const slot = this.$refs.captcha;
    if (!this.captcha || !slot || !api || typeof api.render !== "function") {
      this.captchaFailed = true;
      return;
    }
    this.captchaWidget = api.render(slot, {
      sitekey: this.captcha.siteKey,
      callback: (token) => {
        this.captchaToken = token;
      },
      "expired-callback": () => {
        this.captchaToken = "";
      },
      "error-callback": () => {
        this.captchaToken = "";
      },
    });
  },
  // An answer is spent once the provider has seen it, refused or not.
  resetCaptcha() {
    this.captchaToken = "";
    const api = window.turnstile || window.grecaptcha;
    if (this.captchaWidget !== null && api && typeof api.reset === "function") {
      api.reset(this.captchaWidget);
    }
  },
  async submit() {
    if (this.busy || this.locked > 0) {
      return;
    }
    if (this.captcha && !this.captchaToken) {
      this.error = t("login.captchaRequired");
      return;
    }
    this.busy = true;
    this.error = "";
    try {
      await api("/api/auth/login", {
        method: "POST",
        body: { username: this.username, password: this.password, captcha: this.captchaToken },
      });
      location.assign(this.target);
    } catch (err) {
      if (this.captcha) {
        this.resetCaptcha();
      }
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
