// Starts the dashboard: the CSP build of Alpine, the directives that read their
// value as a plain word, the stores the pages share, then the router.
import Alpine from "/static/lib/alpine.csp.js";
import "./ui/directives.js";
import { api } from "./ui/api.js";
import { bindConfirm } from "./ui/dialog.js";
import { load, locale, setLocale, t } from "./ui/i18n.js";
import { startRouter } from "./router.js";
import { toastStore } from "./ui/toast.js";

Alpine.data("shell", () => ({
  theme: document.documentElement.getAttribute("data-bs-theme") || "light",
  locale: locale(),
  menu: false,
  pw: { current: "", next: "", again: "", error: "", busy: false },
  isCurrent(href) {
    return this.$store.route.nav === href;
  },
  toggleMenu() {
    this.menu = !this.menu;
  },
  closeMenu() {
    this.menu = false;
  },
  label(key) {
    return t(key);
  },
  toggleTheme() {
    this.theme = this.theme === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-bs-theme", this.theme);
    try {
      localStorage.setItem("rl-theme", this.theme);
    } catch (err) {
      // A browser that refuses storage still gets the switch for this page.
    }
  },
  async switchLocale() {
    try {
      await setLocale(this.locale === "en" ? "id" : "en");
    } catch (err) {
      window.Alpine.store("toasts").error(t("locale.failed"));
    }
  },
  async signOut() {
    try {
      await api("/api/auth/logout", { method: "POST", body: {} });
    } catch (err) {
      // The cookie may already be gone; the login page is still the answer.
    }
    location.assign("/login");
  },
  openPassword() {
    this.menu = false;
    this.pw = { current: "", next: "", again: "", error: "", busy: false };
    this.$refs.password.showModal();
    this.$refs.passwordCurrent.focus();
  },
  setPw(field, event) {
    this.pw[field] = event.target.value;
  },
  closePassword() {
    this.$refs.password.close();
    this.forgetPassword();
  },
  forgetPassword() {
    this.pw = { current: "", next: "", again: "", error: "", busy: false };
  },
  async savePassword() {
    this.pw.error = "";
    if (this.pw.next !== this.pw.again) {
      this.pw.error = t("password.mismatch");
      return;
    }
    this.pw.busy = true;
    try {
      await api("/api/auth/change-password", {
        method: "POST",
        body: { currentPassword: this.pw.current, newPassword: this.pw.next },
      });
      this.closePassword();
      window.Alpine.store("toasts").success(t("password.changed"));
    } catch (err) {
      this.pw.error = err.message;
    } finally {
      this.pw.busy = false;
    }
  },
}));

Alpine.store("toasts", toastStore());
Alpine.store("route", { path: "", params: {}, nav: "/dashboard/endpoint" });
window.Alpine = Alpine;

bindConfirm();
await load();
document.documentElement.lang = locale();
Alpine.start();
startRouter();
