// Settings: login, single sign-on and routing. Every card writes only its own
// keys, and a write answers the merged view the card is drawn from.
import { api, patchSettings } from "/static/js/ui/api.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";
import { formatMethods } from "/static/js/ui/format.js";
import { clampInt, fieldMethods, setPath } from "/static/js/ui/fields.js";

const modes = ["password", "oidc", "saml", "sso", "both"];

window.Alpine.data("settingsPage", () => Object.assign({
  loading: true,
  error: "",
  busy: false,
  settings: {},
  form: {
    requireLogin: true,
    authMode: "password",
    ssoType: "oidc",
    oidcIssuerUrl: "",
    oidcClientId: "",
    oidcScopes: "openid profile email",
    oidcLoginLabel: "Sign in with OIDC",
    samlEntryPoint: "",
    samlIssuer: "",
    samlCert: "",
    samlIdpEntityId: "",
    samlLoginLabel: "Sign in with SAML SSO",
    samlAttributeEmail: "email",
    samlAttributeName: "name",
    fallbackStrategy: "fill-first",
    stickyRoundRobinLimit: 3,
    comboStrategy: "fallback",
    comboStickyRoundRobinLimit: 1,
  },
  secret: { oidcClientSecret: "" },
  fieldError: "",

  init() {
    this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      this.apply(await api("/api/settings"));
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  apply(view) {
    this.settings = view;
    for (const key of Object.keys(this.form)) {
      if (view[key] !== undefined) {
        this.form[key] = view[key];
      }
    }
  },


  setBool(path, event) {
    setPath(this, path, event.target.checked);
  },

  setMode(value) {
    this.form.authMode = value;
  },

  setType(value) {
    this.form.ssoType = value;
  },

  modeOptions() {
    return modes;
  },

  modeName(value) {
    return t("settings.mode" + value.charAt(0).toUpperCase() + value.slice(1));
  },

  async patch(body, done) {
    this.busy = true;
    this.fieldError = "";
    try {
      this.apply(await patchSettings(body));
      toast().success(t(done));
      return true;
    } catch (err) {
      this.fieldError = err.message;
      return false;
    } finally {
      this.busy = false;
    }
  },

  savedSecurity() {
    return this.patch({ requireLogin: this.form.requireLogin }, "settings.savedSecurity");
  },

  oidcAnswered(hasSecret) {
    const ssoOnly = this.form.authMode !== "password";
    if (!ssoOnly || this.settings.oidcConfigured === true) {
      return true;
    }
    return hasSecret && this.form.oidcIssuerUrl.trim() !== "" && this.form.oidcClientId.trim() !== "";
  },

  savedOIDC() {
    const secret = this.secret.oidcClientSecret;
    if (!this.oidcAnswered(secret !== "")) {
      this.fieldError = t("settings.oidcNeedsSecret");
      return;
    }
    const body = {
      authMode: this.form.authMode,
      ssoType: "oidc",
      oidcIssuerUrl: this.form.oidcIssuerUrl.trim(),
      oidcClientId: this.form.oidcClientId.trim(),
      oidcScopes: this.form.oidcScopes.trim(),
      oidcLoginLabel: this.form.oidcLoginLabel.trim(),
    };
    if (secret !== "") {
      body.oidcClientSecret = secret;
    }
    this.patch(body, "settings.savedSSO").then((ok) => {
      if (ok) {
        this.secret.oidcClientSecret = "";
      }
    });
  },

  savedSAML() {
    this.patch({
      authMode: this.form.authMode,
      ssoType: "saml",
      samlEntryPoint: this.form.samlEntryPoint.trim(),
      samlIssuer: this.form.samlIssuer.trim(),
      samlCert: this.form.samlCert.trim(),
      samlIdpEntityId: this.form.samlIdpEntityId.trim(),
      samlLoginLabel: this.form.samlLoginLabel.trim(),
      samlAttributeEmail: this.form.samlAttributeEmail.trim(),
      samlAttributeName: this.form.samlAttributeName.trim(),
    }, "settings.savedSSO");
  },

  savedRouting() {
    const body = { fallbackStrategy: this.form.fallbackStrategy, comboStrategy: this.form.comboStrategy };
    const sticky = clampInt(this.form.stickyRoundRobinLimit, 1, 100);
    if (sticky !== null) {
      body.stickyRoundRobinLimit = sticky;
    }
    const comboSticky = clampInt(this.form.comboStickyRoundRobinLimit, 1, 1000);
    if (comboSticky !== null) {
      body.comboStickyRoundRobinLimit = comboSticky;
    }
    this.patch(body, "settings.savedRouting");
  },

  ssoType() {
    return this.form.ssoType;
  },

  redirectURI() {
    return location.origin + "/api/auth/oidc/callback";
  },

  acsURI() {
    return location.origin + "/api/auth/saml/acs";
  },

  metadataURI() {
    return location.origin + "/api/auth/saml/metadata";
  },

}, formatMethods, fieldMethods));
