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
  if (!data.state || (!data.code && !data.error)) {
    show("This page needs the address the provider sent you to. Start the sign-in again from the dashboard.");
    return;
  }
  try {
    const res = await fetch("/api/oauth/callback", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(data),
    });
    show(res.ok ? "Signed in. You can close this tab and return to the dashboard." : "The sign-in did not finish. Return to the dashboard and start it again.");
  } catch {
    show("The router could not be reached. Return to the dashboard and start the sign-in again.");
  }
}

finish();
