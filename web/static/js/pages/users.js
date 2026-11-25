// Users: the standard-user accounts and the keys assigned to each. Every route
// behind this page is admin-only, and the server refuses it to a standard user.
import { api } from "/static/js/ui/api.js";
import { ask } from "/static/js/ui/dialog.js";
import { t } from "/static/js/ui/i18n.js";
import { toast } from "/static/js/ui/toast.js";

window.Alpine.data("usersPage", () => ({
  loading: true,
  error: "",
  users: [],
  userForm: null,

  async init() {
    await this.load();
  },

  async load() {
    this.loading = true;
    this.error = "";
    try {
      const listing = await api("/api/users");
      this.users = listing.users || [];
    } catch (err) {
      this.error = err.message;
    } finally {
      this.loading = false;
    }
  },

  openUser(user) {
    this.userForm = {
      id: user ? user.id : "",
      name: user ? user.username : "",
      email: user ? user.email || "" : "",
      password: "",
      error: "",
      busy: false,
    };
    this.$refs.userDlg.showModal();
    this.$refs.userName.focus();
  },

  closeUser() {
    this.$refs.userDlg.close();
  },

  // forgetUser runs on the dialog's own close event, so a typed password is
  // dropped for Escape and the corner button as well as Cancel.
  forgetUser() {
    this.userForm = null;
  },

  setUser(field, event) {
    this.userForm[field] = event.target.value;
  },

  async saveUser() {
    const form = this.userForm;
    form.error = "";
    form.busy = true;
    try {
      const body = { name: form.name, email: form.email };
      if (form.password !== "") {
        body.password = form.password;
      }
      if (form.id) {
        await api("/api/users/" + encodeURIComponent(form.id), { method: "PUT", body: body });
      } else {
        await api("/api/users", { method: "POST", body: body });
      }
      this.closeUser();
      await this.load();
      toast().success(t("users.saved"));
    } catch (err) {
      form.error = err.message;
    } finally {
      form.busy = false;
    }
  },

  async removeUser(user) {
    const yes = await ask({ title: t("users.deleteTitle"), message: t("users.deleteBody"), confirm: t("common.delete") });
    if (!yes) {
      return;
    }
    try {
      await api("/api/users/" + encodeURIComponent(user.id), { method: "DELETE" });
      await this.load();
      toast().success(t("users.deleted"));
    } catch (err) {
      toast().error(err.message);
    }
  },
}));
