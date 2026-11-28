// Who the dashboard is shown to. One read serves the shell and the page, and a
// read that fails answers the standard-user view: the API narrows every call
// again, but a page must not offer admin controls on a guess.
import { api } from "/static/js/ui/api.js";

let pending = null;

// roleMethods is the one reading of the role a page holds in `this.role`: every
// role but "user" is shown the administrator's controls.
export const roleMethods = {
  isAdmin() {
    return this.role !== "user";
  },
};

export function session() {
  if (!pending) {
    pending = api("/api/auth/status").then(
      (status) => ({
        // A local request with login off has no session and no role; it acts as admin.
        role: status.role === "user" ? "user" : "admin",
        user: status.user || "",
        needsPassword: status.needsPassword === true,
      }),
      () => {
        pending = null;
        return { role: "user", user: "", needsPassword: false };
      },
    );
  }
  return pending;
}
