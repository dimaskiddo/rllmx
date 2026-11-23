// The one-time code in the address is read and removed before anything else
// runs: the translation file loads after, so the code never waits in the URL.
import { load, locale, t } from "./ui/i18n.js";

const message = document.getElementById("message");

function show(text) {
  message.textContent = text;
}

function read() {
  const params = new URLSearchParams(location.search);
  const frag = new URLSearchParams(location.hash.replace(/^#/, ""));
  const pick = (name) => params.get(name) || frag.get(name) || "";
  return { state: pick("state"), code: pick("code") || pick("token"), error: pick("error") };
}

async function finish() {
  const data = read();
  history.replaceState(null, "", location.pathname);
  await load().catch(() => null);
  document.documentElement.lang = locale();
  show(t("callback.finishing"));
  if (!data.state || (!data.code && !data.error)) {
    show(t("callback.needsAddress"));
    return;
  }
  try {
    const res = await fetch("/api/oauth/callback", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    });
    show(res.ok ? t("callback.done") : t("callback.failed"));
  } catch {
    show(t("callback.unreachable"));
  }
}

finish();
