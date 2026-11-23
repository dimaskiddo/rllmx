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
})();
