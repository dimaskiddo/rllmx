// Runs in the head, before the first paint, so a dark dashboard never flashes
// light. Storage can throw in a private window, hence the guard.
(function () {
  var theme = null;
  try {
    theme = localStorage.getItem("rl-theme");
  } catch (e) {
    theme = null;
  }
  if (theme !== "light" && theme !== "dark") {
    theme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  document.documentElement.setAttribute("data-bs-theme", theme);
  // The rail width also lands before the first paint, so the sidebar never
  // jumps wider once the scripts run.
  var nav = null;
  try {
    nav = localStorage.getItem("rl-nav");
  } catch (e) {
    nav = null;
  }
  document.documentElement.setAttribute("data-rl-nav", nav === "rail" ? "rail" : "full");
})();
