// The toast stack lives in an Alpine store so any component can raise one. An
// error stays twice as long, because it is the one worth reading.
const durations = { error: 8000, success: 5000, info: 5000 };

export function toastStore() {
  return {
    items: [],
    next: 1,
    push(text, kind) {
      const id = this.next++;
      this.items.push({ id: id, text: text, kind: kind });
      this.raise();
      setTimeout(this.drop.bind(this), durations[kind] || 5000, id);
    },
    drop(id) {
      this.items = this.items.filter((item) => item.id !== id);
      if (this.items.length === 0) {
        this.lower();
      }
    },
    // The stack is a manual popover, so it is drawn in the top layer. Showing it
    // again after a modal opened puts it above that modal's backdrop; without
    // this a toast raised under an open dialog is dimmed behind it.
    raise() {
      const el = document.getElementById("rl-toasts");
      if (el && el.showPopover) {
        if (el.matches(":popover-open")) {
          el.hidePopover();
        }
        el.showPopover();
      }
    },
    lower() {
      const el = document.getElementById("rl-toasts");
      if (el && el.hidePopover && el.matches(":popover-open")) {
        el.hidePopover();
      }
    },
    error(text) {
      this.push(text, "error");
    },
    success(text) {
      this.push(text, "success");
    },
    info(text) {
      this.push(text, "info");
    },
  };
}

export function toast() {
  return window.Alpine.store("toasts");
}
