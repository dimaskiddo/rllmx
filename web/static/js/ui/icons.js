// The vendored Lucide icons, one entry per file in lib/lucide/icons/. A name
// not listed here is a mistake in the markup, and tests/web_test.go fails it
// before a browser ever sees it.
import createElement from "/static/lib/lucide/createElement.mjs";

const icons = {
  "activity": () => import("/static/lib/lucide/icons/activity.mjs"),
  "arrow-down": () => import("/static/lib/lucide/icons/arrow-down.mjs"),
  "arrow-up": () => import("/static/lib/lucide/icons/arrow-up.mjs"),
  "check": () => import("/static/lib/lucide/icons/check.mjs"),
  "clock": () => import("/static/lib/lucide/icons/clock.mjs"),
  "copy": () => import("/static/lib/lucide/icons/copy.mjs"),
  "external-link": () => import("/static/lib/lucide/icons/external-link.mjs"),
  "eye": () => import("/static/lib/lucide/icons/eye.mjs"),
  "eye-off": () => import("/static/lib/lucide/icons/eye-off.mjs"),
  "globe": () => import("/static/lib/lucide/icons/globe.mjs"),
  "key-round": () => import("/static/lib/lucide/icons/key-round.mjs"),
  "layers": () => import("/static/lib/lucide/icons/layers.mjs"),
  "log-in": () => import("/static/lib/lucide/icons/log-in.mjs"),
  "log-out": () => import("/static/lib/lucide/icons/log-out.mjs"),
  "moon": () => import("/static/lib/lucide/icons/moon.mjs"),
  "pause": () => import("/static/lib/lucide/icons/pause.mjs"),
  "pencil": () => import("/static/lib/lucide/icons/pencil.mjs"),
  "plus": () => import("/static/lib/lucide/icons/plus.mjs"),
  "server": () => import("/static/lib/lucide/icons/server.mjs"),
  "sun": () => import("/static/lib/lucide/icons/sun.mjs"),
  "trash": () => import("/static/lib/lucide/icons/trash.mjs"),
  "triangle-exclamation-point": () => import("/static/lib/lucide/icons/triangle-exclamation-point.mjs"),
  "user": () => import("/static/lib/lucide/icons/user.mjs"),
  "x": () => import("/static/lib/lucide/icons/x.mjs"),
};

// icon fills an empty element with the SVG for a name. It is idempotent: a
// second call leaves the drawn icon alone.
export async function icon(el, name) {
  const load = icons[name];
  if (!load || el.firstElementChild) {
    return;
  }
  const svg = createElement((await load()).default);
  svg.classList.add("rl-icon");
  svg.setAttribute("aria-hidden", "true");
  el.replaceChildren(svg);
}
