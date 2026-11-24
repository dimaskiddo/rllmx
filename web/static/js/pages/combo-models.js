// The ordered model editor a combo shares between its create and edit dialogs:
// drag to reorder, arrows for the keyboard, and one free-text field that takes
// an alias or a provider/model.
import Sortable from "/static/lib/sortablejs/sortable.core.esm.js";

let handle = null;

export const modelEditorMethods = {
  // sortModels binds drag-and-drop once per open dialog. The list is redrawn by
  // Alpine, so the handler moves the array and never the nodes.
  sortModels() {
    const list = this.$refs.modelList;
    if (!list || handle) {
      return;
    }
    handle = Sortable.create(list, {
      handle: "[data-drag]",
      animation: 120,
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

  setModelAt(index, event) {
    const value = event.target.value.trim();
    if (!value) {
      this.form.models.splice(index, 1);
      return;
    }
    this.form.models[index] = value;
  },

  addModel() {
    const value = this.form.add.trim();
    if (!value || this.form.models.indexOf(value) >= 0) {
      return;
    }
    this.form.models.push(value);
    this.form.add = "";
  },

  removeModel(index) {
    this.form.models.splice(index, 1);
  },
};
