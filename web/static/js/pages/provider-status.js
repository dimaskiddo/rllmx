// Pure helpers for the provider pages. This file imports nothing and touches no
// global, so a script can load it on its own and check the rules below.

// renumber swaps the connection at index with its neighbour and numbers the
// whole list from 1. changes holds only the rows whose number differs from the
// one they carry now, so a move sends no more requests than it must. It returns
// null when the neighbour is outside the list.
export function renumber(list, index, direction) {
  const other = index + direction;
  if (other < 0 || other >= list.length) {
    return null;
  }
  const next = list.slice();
  const held = next[index];
  next[index] = next[other];
  next[other] = held;
  const changes = [];
  next.forEach((conn, position) => {
    if (conn.priority !== position + 1) {
      changes.push({ id: conn.id, priority: position + 1 });
    }
  });
  return { list: next, changes: changes };
}

// psdRows turns the stored provider data into editable rows. A masked value or
// an object is shown but never sent back. Each row remembers the key, the text
// and the kind it started as, so a save can tell what changed.
export function psdRows(data, skip) {
  const out = [];
  for (const key of Object.keys(data || {}).sort()) {
    if (skip && skip.indexOf(key) >= 0) {
      continue;
    }
    const value = data[key];
    const simple = typeof value === "string" || typeof value === "number" || typeof value === "boolean";
    const text = simple ? String(value) : JSON.stringify(value);
    out.push({
      key: key, value: text, readonly: !simple || value === "***",
      origKey: key, origValue: text, kind: typeof value,
    });
  }
  return out;
}

// psdTyped gives a row's text back the kind it was stored as, so editing one
// field never turns a number or a flag into a string. A text that no longer
// reads as that kind is sent as the text.
function psdTyped(row) {
  if (row.kind === "number" && row.value.trim() !== "" && isFinite(Number(row.value))) {
    return Number(row.value);
  }
  if (row.kind === "boolean" && (row.value === "true" || row.value === "false")) {
    return row.value === "true";
  }
  return row.value;
}

// psdBody is the providerSpecificData of an update. The server merges key by
// key and removes a key only on null, so a row that was deleted or renamed away
// is sent as null, and a row nobody touched is not sent at all. editable lists
// the keys the dialog opened with.
export function psdBody(rows, editable) {
  const out = {};
  const kept = new Set();
  for (const row of rows) {
    if (row.readonly || row.key === "") {
      continue;
    }
    kept.add(row.key);
    if (row.origKey === row.key && row.origValue === row.value) {
      continue;
    }
    out[row.key] = row.value === "" ? null : psdTyped(row);
  }
  for (const key of editable || []) {
    if (!kept.has(key) && !(key in out)) {
      out[key] = null;
    }
  }
  return out;
}

// modelRow reads one row of the registry list. That list names the model in
// `model`; the custom list says `id`, and the two must not be mixed up.
export function modelRow(m) {
  return {
    id: m.model, name: m.name || "", source: m.source || "builtin", type: m.kind || "",
    full: m.fullModel || "", caps: m.caps || null,
  };
}

// sortModels lists the built-in models first and the custom ones after them,
// each group in the order the server sent. The server merges a custom model
// into the place of the built-in one it replaces, so its order mixes the two.
export function sortModels(items) {
  const custom = (m) => (m.source === "custom" ? 1 : 0);
  return items.slice().sort((a, b) => custom(a) - custom(b));
}

// checkAllowsSave is true once a key check passed or said it cannot judge the
// provider. Anything else is a refusal and keeps the connection unsaved.
export function checkAllowsSave(check) {
  return !!check && (check.valid || check.unsupported === true);
}

// cooldownOf is the soonest future model lock on a connection, or 0.
export function cooldownOf(conn, now) {
  let soonest = 0;
  for (const key of Object.keys(conn)) {
    if (key.indexOf("modelLock_") !== 0) {
      continue;
    }
    const until = new Date(conn[key]).getTime();
    if (isFinite(until) && until > now && (soonest === 0 || until < soonest)) {
      soonest = until;
    }
  }
  return soonest;
}

// span writes a duration in milliseconds as "1h 5m", "2m 3s" or "9s".
export function span(ms) {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) {
    return seconds + "s";
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return seconds % 60 ? minutes + "m " + (seconds % 60) + "s" : minutes + "m";
  }
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? hours + "h " + (minutes % 60) + "m" : hours + "h";
}

// mergeConnection puts the answer of an update into the list in place of the
// row it changed. The list adds a display name for a node connection that has
// none and the update's answer does not, so an empty name keeps the old one.
// Rows other than the changed one are the same objects, so nothing else redraws.
export function mergeConnection(list, updated) {
  return list.map((conn) => {
    if (conn.id !== updated.id) {
      return conn;
    }
    const merged = Object.assign({}, conn, updated);
    if (!updated.name && conn.name) {
      merged.name = conn.name;
    }
    return merged;
  });
}
