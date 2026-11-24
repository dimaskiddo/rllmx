// The ordered model editor a combo shares between its create and edit dialogs:
// drag to reorder, arrows for the keyboard, and one free-text field that takes
// an alias or a provider/model.
import Sortable from "/static/lib/sortablejs/sortable.core.esm.js";

let handle = null;

export const modelEditorMethods = {
  // sortModels binds drag-and-drop once per open dialog. The list is keyed by
  // model, so Alpine redraws it in the array's new order; onEnd moves the array
  // and never the nodes.
  sortModels() {
    const list = this.$refs.modelList;
    if (!list || handle) {
      return;
    }
    handle = Sortable.create(list, {
      handle: "[data-drag]",
      animation: 120,
      // The handle is a button, which Firefox will not start a native drag from.
      forceFallback: true,
      ghostClass: "rl-drag-ghost",
      chosenClass: "rl-drag-chosen",
      dragClass: "rl-drag-active",
      fallbackClass: "rl-drag-active",
      onEnd: (evt) => this.moveTo(evt.oldIndex, evt.newIndex),
    });
  },

  stopSortable() {
    if (handle) {
      handle.destroy();
      handle = null;
    }
  },

  moveTo(from, to) {
    if (from === to || from < 0 || to < 0) {
      return;
    }
    const models = this.form.models;
    const moved = models.splice(from, 1)[0];
    models.splice(to, 0, moved);
  },

  moveModel(index, delta) {
    this.moveTo(index, index + delta);
  },

  // The list is keyed by model, so a value another row already holds would be a
  // duplicate key; the edited row goes instead.
  setModelAt(index, event) {
    const value = event.target.value.trim();
    const at = this.form.models.indexOf(value);
    if (!value || (at >= 0 && at !== index)) {
      this.form.models.splice(index, 1);
      return;
    }
    this.form.models[index] = value;
  },

  // A copied combo arrives one model per line; a typed list may use commas.
  splitModels(text) {
    return text.split(/[\n,]+/).map((value) => value.trim()).filter(Boolean);
  },

  addModels(list) {
    for (const value of list) {
      if (this.form.models.length >= 50) {
        return;
      }
      if (this.form.models.indexOf(value) < 0) {
        this.form.models.push(value);
      }
    }
  },

  addModel() {
    this.addModels(this.splitModels(this.form.add));
    this.form.add = "";
  },

  // A paste holding more than one model fills rows instead of pasting a line of
  // text into a single box.
  pasteModels(event) {
    const text = (event.clipboardData || window.clipboardData).getData("text");
    const list = this.splitModels(text);
    if (list.length < 2) {
      return;
    }
    event.preventDefault();
    this.addModels(list);
    this.form.add = "";
  },

  removeModel(index) {
    this.form.models.splice(index, 1);
  },
};
