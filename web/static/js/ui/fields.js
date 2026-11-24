// setPath writes a value at a dotted path below target ("add.name"). A page
// component calls it from one setField method, so a text input is bound with
// setField('add.name', $event) instead of a method of its own per field.
export function setPath(target, path, value) {
  const parts = path.split(".");
  let node = target;
  for (let i = 0; i < parts.length - 1; i++) {
    node = node[parts[i]];
  }
  node[parts[parts.length - 1]] = value;
}

// clampInt reads text as a whole number inside low..high, or answers null when
// it is not a number at all.
export function clampInt(value, low, high) {
  const n = parseInt(value, 10);
  if (isNaN(n)) {
    return null;
  }
  return Math.max(low, Math.min(high, n));
}

// fieldMethods is the one setField a page mixes in for its text inputs.
export const fieldMethods = {
  setField(path, event) {
    setPath(this, path, event.target.value);
  },
};
