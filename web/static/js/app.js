// Starts the dashboard: the CSP build of Alpine, the directives that read their
// value as a plain word, the stores the pages share, then the router.
import Alpine from "/static/lib/alpine.csp.js";
import "./ui/directives.js";
import { api } from "./ui/api.js";
import { roleMethods, session } from "./ui/session.js";
import { bindConfirm } from "./ui/dialog.js";
import { load, locale, setLocale, t } from "./ui/i18n.js";
import { startRouter } from "./router.js";
import { toast, toastStore } from "./ui/toast.js";
import { formatMethods } from "./ui/format.js";

Alpine.data("shell", () => Object.assign({
  theme: document.documentElement.getAttribute("data-bs-theme") || "light",
  locale: locale(),
  menu: false,
  navOpen: false,
  rail: document.documentElement.getAttribute("data-rl-nav") === "rail",
  role: "user",
  userName: "",
  needsPassword: false,
  pw: { current: "", next: "", again: "", error: "", busy: false },
  async init() {
    const who = await session();
    this.role = who.role;
    this.userName = who.user;
    this.needsPassword = who.needsPassword;
    this.$watch("$store.route.nav", () => this.closeNav());
  },
  isCurrent(href) {
    return this.$store.route.nav === href;
  },
  toggleMenu() {
    this.menu = !this.menu;
  },
  // One button: a drawer on a phone, a rail on a wider screen.
  toggleNav() {
    if (window.matchMedia("(max-width: 767.98px)").matches) {
      this.navOpen = !this.navOpen;
      return;
    }
    this.rail = !this.rail;
    document.documentElement.setAttribute("data-rl-nav", this.rail ? "rail" : "full");
    try {
      localStorage.setItem("rl-nav", this.rail ? "rail" : "full");
    } catch (err) {
      // A browser that refuses storage still gets the switch for this page.
    }
  },
  closeNav() {
    this.navOpen = false;
  },
  navClass() {
    return this.navOpen ? "rl-nav-open" : "";
  },
  navExpanded() {
    if (window.matchMedia("(max-width: 767.98px)").matches) {
      return this.navOpen ? "true" : "false";
    }
    return this.rail ? "false" : "true";
  },
  closeMenu() {
    this.menu = false;
  },
  toggleTheme() {
    this.theme = this.theme === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-bs-theme", this.theme);
    window.dispatchEvent(new Event("rl-theme"));
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
      toast().error(t("locale.failed"));
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
        body: { currentPassword: this.needsPassword ? "" : this.pw.current, newPassword: this.pw.next },
      });
      this.closePassword();
      toast().success(t("password.changed"));
      window.dispatchEvent(new Event("rl-password-changed"));
    } catch (err) {
      this.pw.error = err.message;
    } finally {
      this.pw.busy = false;
    }
  },
}, formatMethods, roleMethods));

Alpine.store("toasts", toastStore());
Alpine.store("route", { path: "", params: {}, nav: "/dashboard/endpoint" });
window.Alpine = Alpine;

bindConfirm();
await load();
document.documentElement.lang = locale();
Alpine.start();
startRouter();
