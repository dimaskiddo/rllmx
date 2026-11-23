// The dashboard's one way to call /api. The session is a same-site cookie, and
// the server refuses a cross-site write, so no token or header is added here.

export class ApiError extends Error {
  constructor(message, status, data) {
    super(message);
    this.status = status;
    this.data = data || null;
  }
}

// nextPath reads the `next` the login page was sent with and refuses anything
// that is not one of our own dashboard paths: `//host`, `\host` and an
// absolute address all fall back to the dashboard.
export function nextPath(search) {
  const raw = new URLSearchParams(search).get("next") || "";
  const ours = raw === "/dashboard" || raw.startsWith("/dashboard/") || raw.startsWith("/dashboard?");
  if (!ours || raw.includes("\\")) {
    return "/dashboard";
  }
  // Resolve it the way the browser will, so /dashboard/../x or /dashboard//host
  // cannot end up somewhere else than the dashboard.
  let url;
  try {
    url = new URL(raw, location.origin);
  } catch (err) {
    return "/dashboard";
  }
  const inside = url.pathname === "/dashboard" || url.pathname.startsWith("/dashboard/");
  if (url.origin !== location.origin || !inside || url.pathname.startsWith("//")) {
    return "/dashboard";
  }
  return url.pathname + url.search;
}

export async function api(path, options) {
  const opts = Object.assign({ method: "GET", credentials: "same-origin" }, options || {});
  if (opts.body !== undefined && typeof opts.body !== "string") {
    opts.body = JSON.stringify(opts.body);
    opts.headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
  }
  const res = await fetch(path, opts);
  const type = res.headers.get("Content-Type") || "";
  const data = type.indexOf("application/json") >= 0 ? await res.json().catch(() => null) : null;
  if (res.status === 401 && path.indexOf("/api/auth/") !== 0) {
    location.assign("/login?next=" + encodeURIComponent(location.pathname + location.search));
    throw new ApiError("Signed out", 401, data);
  }
  if (!res.ok) {
    throw new ApiError((data && data.error) || "Request failed (" + res.status + ")", res.status, data);
  }
  return data;
}
