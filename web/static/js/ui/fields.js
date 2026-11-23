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
