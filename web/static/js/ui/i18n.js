// Strings live in /static/i18n/<locale>.json and the locale is the readable
// cookie /api/locale sets. A missing key shows the key, so a gap is visible and
// never breaks a page.

export const locales = ["en", "id"];

let messages = {};

export function locale() {
  const found = document.cookie.match(/(?:^|;\s*)locale=([^;]+)/);
  const value = found ? decodeURIComponent(found[1]) : "en";
  return locales.indexOf(value) >= 0 ? value : "en";
}

export async function load() {
  const current = locale();
  const res = await fetch("/static/i18n/" + current + ".json", { credentials: "same-origin" });
  messages = res.ok ? await res.json() : {};
  return current;
}

export function t(key) {
  const value = messages[key];
  return typeof value === "string" ? value : key;
}

// setLocale stores the choice and reloads. It throws when the server refuses,
// so the caller can say so instead of reloading into the same language.
export async function setLocale(next) {
  const res = await fetch("/api/locale", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ locale: next }),
  });
  if (!res.ok) {
    throw new Error("locale");
  }
  location.reload();
}
