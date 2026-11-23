// One confirm dialog for the whole dashboard, built on the native element so
// focus trapping and Escape come from the browser. The shell holds it; ask()
// fills it in and resolves with the answer.
import { t } from "./i18n.js";

export function ask(options) {
  const dialog = document.getElementById("rl-confirm");
  // A second click while the first question is open must not become a second
  // "yes": both promises would resolve true and the action would run twice.
  if (!dialog || dialog.open) {
    return Promise.resolve(false);
  }
  dialog.querySelector("[data-confirm='title']").textContent = options.title;
  const message = dialog.querySelector("[data-confirm='message']");
  message.textContent = options.message || "";
  message.hidden = !options.message;
  const yes = dialog.querySelector("[data-confirm='yes']");
  yes.textContent = options.confirm || t("common.confirm");
  yes.className = options.danger === false ? "btn btn-primary" : "btn btn-danger";
  dialog.querySelector("[data-confirm='no']").textContent = t("common.cancel");
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener(
      "close",
      () => resolve(dialog.returnValue === "yes"),
      { once: true },
    );
  });
}

// bindConfirm wires the dialog's buttons once, at start-up: closing with a
// returnValue is the only way out, so Escape and a backdrop click answer no.
export function bindConfirm() {
  const dialog = document.getElementById("rl-confirm");
  if (!dialog) {
    return;
  }
  dialog.querySelector("[data-confirm='yes']").addEventListener("click", () => {
    dialog.close("yes");
  });
  dialog.querySelector("[data-confirm='no']").addEventListener("click", () => {
    dialog.close("no");
  });
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    dialog.close("no");
  });
}
