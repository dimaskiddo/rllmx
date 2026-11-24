// The History-API router behind /dashboard/*. A page is a fragment from our own
// static folder plus the module that registers its Alpine component; neither is
// built from data.
import { t } from "./ui/i18n.js";

let routes = null;
// renderSeq numbers every render. A fragment that arrives after a newer click
// or a back press is dropped, so the page on screen is the one the address names.
let renderSeq = 0;
let rendered = false;

async function table() {
  if (!routes) {
    const res = await fetch("/static/js/routes.json", { credentials: "same-origin" });
    if (!res.ok) {
      throw new Error("routes");
    }
    routes = await res.json();
  }
  return routes;
}

// match compares a "/dashboard/providers/:id" pattern with a path and returns
// its parameters, or null.
function match(pattern, path) {
  const want = pattern.split("/");
  const got = path.split("/");
  if (want.length !== got.length) {
    return null;
  }
  const params = {};
  for (let i = 0; i < want.length; i++) {
    if (want[i].charAt(0) === ":") {
      if (!got[i]) {
        return null;
      }
      try {
        params[want[i].slice(1)] = decodeURIComponent(got[i]);
      } catch (err) {
        return null;
      }
    } else if (want[i] !== got[i]) {
      return null;
    }
  }
  return params;
}

// navRoots are the sidebar entries, so a page marks its own entry current.
const navRoots = [
  "/dashboard/endpoint",
  "/dashboard/providers",
  "/dashboard/combos",
  "/dashboard/token-saver",
  "/dashboard/skills",
  "/dashboard/usage",
  "/dashboard/quota",
  "/dashboard/pricing",
  "/dashboard/users",
  "/dashboard/settings",
  "/dashboard/console-log",
];

function navFor(path) {
  for (const root of navRoots) {
    if (path === root || path.indexOf(root + "/") === 0) {
      return root;
    }
  }
  return "/dashboard/endpoint";
}

async function resolve(path) {
  for (const route of await table()) {
    const params = match(route.path, path);
    if (params) {
      return { page: route.page, params: params };
    }
  }
  return { page: "not-found", params: {} };
}

async function render() {
  const seq = ++renderSeq;
  const path = location.pathname;
  let route;
  try {
    route = await resolve(path);
  } catch (err) {
    route = { page: "", params: {} };
  }
  const view = document.getElementById("view");
  if (!view || seq !== renderSeq) {
    return;
  }
  let fragment = null;
  try {
    if (route.page === "") {
      throw new Error("routes");
    }
    const [res] = await Promise.all([
      fetch("/static/pages/" + route.page + ".html", { credentials: "same-origin" }),
      import("/static/js/pages/" + route.page + ".js"),
    ]);
    if (res.ok) {
      fragment = new DOMParser().parseFromString(await res.text(), "text/html").body;
    }
    if (seq !== renderSeq) {
      return;
    }
  } catch (err) {
    fragment = null;
  }
  if (seq !== renderSeq) {
    return;
  }
  if (!fragment) {
    const fallback = document.createElement("p");
    fallback.className = "rl-hint rl-empty";
    fallback.textContent = t("common.pageFailed");
    window.Alpine.destroyTree(view);
    view.replaceChildren(fallback);
    return;
  }
  const store = window.Alpine.store("route");
  store.path = path;
  store.params = route.params;
  store.nav = navFor(path);
  // The first view is the page load itself; only a later one moves focus.
  const announce = rendered;
  rendered = true;
  window.Alpine.destroyTree(view);
  view.replaceChildren(...fragment.childNodes);
  window.Alpine.initTree(view);
  // The directives write the heading text on the next tick; read it after.
  window.Alpine.nextTick(() => {
    const heading = view.querySelector("h1");
    document.title = (heading ? heading.textContent + " · " : "") + "Router-LLM";
    if (announce) {
      view.focus({ preventScroll: true });
    }
  });
}

export function startRouter() {
  document.addEventListener("click", (event) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }
    const link = event.target.closest("a[href]");
    if (!link || link.target || link.hasAttribute("download")) {
      return;
    }
    const url = new URL(link.href, location.href);
    const ours = url.pathname === "/dashboard" || url.pathname.indexOf("/dashboard/") === 0;
    if (url.origin !== location.origin || !ours) {
      return;
    }
    event.preventDefault();
    history.pushState({}, "", url.pathname + url.search);
    render();
  });
  window.addEventListener("popstate", render);
  render();
}
