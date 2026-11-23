// Copying a key is the one clipboard use the dashboard has. The clipboard API
// needs a secure context, so a plain-http LAN address falls back to selecting
// the visible field, and the operator presses the browser's own shortcut.
import { t } from "./i18n.js";
import { toast } from "./toast.js";

// copy takes the element to select when the clipboard is out of reach. A caller
// that passes null gets the <code> beside the button that was just pressed, which
// is where every copy button in the dashboard sits.
export async function copy(text, source) {
  if (!source) {
    const pressed = document.activeElement;
    const row = pressed && pressed.closest ? pressed.closest(".rl-inline") : null;
    source = row ? row.querySelector("code") : null;
  }
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      toast().success(t("common.copied"));
      return;
    } catch (err) {
      // Fall through to the selection.
    }
  }
  if (source) {
    const range = document.createRange();
    range.selectNodeContents(source);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    toast().info(t("common.selected"));
    return;
  }
  toast().error(t("common.copyFailed"));
}
