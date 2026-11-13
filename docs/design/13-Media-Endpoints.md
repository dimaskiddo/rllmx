# Design 13 — Media Endpoints (`app/gateway/`)

**Status:** partial

Each endpoint resolves model and account as chat does, then calls a per-provider adapter registered by provider id and modality. Usage rows carry the modality in `endpoint`.

| Endpoint | Notes |
|---|---|
| Embeddings | OpenAI shape; custom embedding nodes supported |
| Images | OpenAI-style and vendor adapters; image models never stream |
| Audio | Speech, transcriptions, voice lists |
| Video | Create, edit, extend, then poll by id |
| Search | Normalizes results across search vendors; SearXNG through `MEDIA_SEARXNG_URL` |
| Fetch | URL to markdown; a vendor extracts it, or the built-in reader converts it; every target goes through the SSRF guard |

---

## Remaining work

| Slice | Scope | Status |
|---|---|---|
| Foundations | Media schema, index, `media/mux`, `app/gateway/media`, embeddings, non-browser images, probes, one usage row per request | done |
| Vendor images | Stability, BFL, fal, HuggingFace, ComfyUI, SD WebUI, Runway, Cloudflare, NanoBanana, SenseNova intl, plus the codex and antigravity bridges | done |
| Audio | TTS, STT, voice lists: `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/voices`, the two dashboard routes | done |
| Video | `/v1/videos/generations\|edits\|extensions` and `GET /v1/videos/{id}`: xAI's shape, OpenRouter and Vertex (Veo) | done |
| Search and fetch | `/v1/search` and `/v1/web/fetch`: 13 dedicated search vendors, 9 chat searches, 6 fetch vendors and the built-in reader; `MEDIA_SEARXNG_URL`; SSRF on every target | done |
| Browser and web media | chatgpt-web, grok-web, gemini-web, muse-web, uc-web, veoaifree-web, and anything the earlier slices defer | todo |

Bounds already built: a 25 MiB upload, a 32 KiB speech input, a 120 s speech timeout, a 300 s transcription timeout, a 120 s video round trip (`MEDIA_VIDEO_TIMEOUT`), a 15 s search, a 2,000-byte query, and a 5 MiB page the built-in reader converts.

A search or fetch names its provider in the body, because the provider is the model there. Its failure locks the account for `websearch:<id>` or `webfetch:<id>` only, so a spent search key never takes chat offline.

A provider with no connection of its own borrows one (`credentialFallback`: ollama-search from ollama), and the reporter attributes the result to the owner.

No search or fetch adapter sends a credential to an address a client chose. A fetch target is refused before any call when it resolves to a private or metadata address, because a vendor reads it from its own network, where our dialer never runs.

The built-in reader is the one path that reads the target here: one GET through a guarded client that follows at most five redirects, each hop dialled through the guard, and a 5 MiB bound.

`MEDIA_SEARXNG_URL` empty serves no SearXNG; when set, its host is permitted for these calls and nothing else. A local SearXNG therefore needs no `UPSTREAM_ALLOW_PRIVATE` entry of its own.

A video create is a billable job. It moves to another account only on 401, 403 or 429, never after a network error, a timeout or a 5xx. Its answer carries `x-9router-connection-id` and `X-Rllm-Connection-Id`, and a poll reads the account from `x-connection-id`, then `?provider=`, then xAI. Runway's video models are served through the images route, as the reference does.

**Done when:** every endpoint passes its end-to-end test against fake providers, and oversized uploads and SSRF targets are rejected.
---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 137 | Gemini audio request (`responseModalities` AUDIO or `speechConfig`) to non-Gemini target: 400; only `image/*` blobs become images | No text where speech asked |
| 128 | Max 16 images, 20 MiB per request, 10 MiB each, duplicates fetched once; stops when cancelled **(enforced by `app/gateway/prefetch`)** | One request cannot exhaust memory |
| 161 | Moved to with media providers | Media providers, not chat providers (scope cut) |
| 293 | Stability's generate request is **multipart/form-data**; the reference sends JSON, which v2beta rejects. **Built** | A request the vendor documents and accepts |
| 294 | fal's `image_size` is an object (`{width,height}`); the reference sends the `"16:9"` string, which fal rejects. **Built** | A request the vendor documents and accepts |
| 295 | Runway's `ratio` is a pixel ratio (`1024:1792`); the reference sends `1:1`, which Runway rejects. **Built** | A request the vendor documents and accepts |
| 296 | An Antigravity answer with no image is an error; the reference answers 200 with an empty `b64_json`. **Built** | A success with no image is not a success |
| 297 | An embeddings request is bounded (2,048 inputs, 32 KiB each); the reference accepts any size. **Built** | One request cannot exhaust memory (`AGENTS.md`) |
| 299 | The scraped and OS-spawned speech providers are out: `edge-tts` and `google-tts` screen-scrape Bing and Translate, and `local-device` runs the OS speech tool through ffmpeg. **Recorded** | Page scraping and process spawns are outside the router's rules (`design doc 11`, `design doc 22`) |
| 300 | `aws-polly` is out: the reference has no handler for its format, so no behavior exists to copy. **Recorded** | A dead entry is not a feature |
| 301 | Codex transcription takes only 16-bit PCM mono 24 kHz WAV and never converts; the reference runs ffmpeg for every format. **Built** | A spawned process is forbidden; a clear 400 beats a silent mismatch |
| 302 | A transcription upload is bounded at 25 MiB; the reference's route accepts the 128 MiB proxy cap. **Built** | One request cannot exhaust memory (`AGENTS.md`) |
| 303 | AssemblyAI gets the key raw in `Authorization`, as its API documents; the reference's own config falls through to `Bearer`. **Built** | The documented form |
| 304 | Speech is served as `audio/mpeg`; the reference writes the non-standard `audio/mp3`. **Built** | The registered media type |
| 305 | The reference's Gemini-native `/v1beta/models/…:generateContent` audio route is not built: the chat route owns that path. Gemini speech is served through `/v1/audio/speech`. **Deferred** | One path, one owner; the TTS adapter already reaches the same model |
| 298 | A no-auth service runs without a connection, and an existing connection is never reported or locked; the reference never touches credentials for these providers. **Built** | A local daemon's failure is not the account's, and a locked connection must not stop a service that needs none |
| 306 | A video create leaves one metadata usage row; the reference records none. **Built** | Every request this router serves is accounted for (`design doc 16`) |
| 307 | A refused video job answers a fixed word; the reference relays up to 2,000 characters of scrubbed provider text. **Built** | Client errors carry no internal detail (`design doc 19`) |
| 308 | A video job id is 1-512 characters of `[A-Za-z0-9._~:-]`; the reference escapes any text into the path. **Built** | One id cannot rewrite the address |
| 310 | `local-fetch` reads the target here: one guarded GET that follows at most five redirects, each hop dialled through the guard, then a local HTML-to-markdown conversion (`golang.org/x/net/html`). **Built** | The reference has no vendor-free path; a fetch that never leaves this process is the one that can be checked at every hop |
| 311 | `MEDIA_SEARXNG_URL` is empty by default and serves no SearXNG; when set, its host is permitted for the media client only. The reference defaults to `http://localhost:8888/search`. **Built** | An unconfigured route is not a broken one, and the address comes from the operator, never a request |
| 312 | `provider_options.baseUrl` from a client is ignored; the address comes from the provider's data or the setting. **Built** | The reference forwards the vendor's API key to a client-chosen public host |
| 313 | A refused search or fetch answers a fixed word; the reference relays up to 200-500 characters of provider text. **Built** | Client errors carry no internal detail (`design doc 19`) |
| 314 | A query is bounded at 2,000 bytes, a fetch target at 2,048, filters at 50, `max_characters` at the provider's own cap; the reference takes them as sent. **Built** | One request cannot exhaust memory (`AGENTS.md`) |
| 315 | OpenAI chat search goes to the provider's own chat address when its data names no search endpoint. **Built** | The reference sends it to an empty address and fails every time |

---

## Findings

One row per lesson; the decision is what the code does because of it.

| Finding | Decision |
|---|---|
| Media is a second request path, not a second pipeline. | `gateway/media` reuses the selector, reporter, refresher, minter and usage writer, and adds one read, one adapter call and one row. |
| A local daemon's failure is not the account's. | A no-auth service runs without a connection, and its connection is never reported or locked. |
| The vendors' own documented request shapes must be sent. | Stability's multipart body, fal's `image_size` object and Runway's pixel ratio replace the reference's; divergence 293-296. |
| Speech adapters must bound memory, and only a stream is not a file. | Every speech adapter reads its whole answer under the 64 MiB cap; only Gemini Live streams. |
| A transcription upload must be read once and re-sent per account. | It is read into memory under a 25 MiB bound, so a fallback never re-reads the client. |
| An SSRF-safe WebSocket client had to be ours. | `app/protocol/websocket` is a minimal RFC 6455 client that takes the caller's dialer and TLS settings. |
| Codex's native image endpoint accepts one shape only. | A tool model with a plain prompt, `n=1`, `size:auto` and a png; a request with reference images goes to the native `/images/edits` endpoint with those images as data URLs and the same controls; anything else is a 400. |
| A Free-plan account is refused before the call, from the token's own claim. | It is passed over without locking the account. |
| A video create is a billable job, so a re-send can pay twice. | It rotates only on 401, 403 or 429, the statuses a provider raises before it starts a job; a timeout or a 5xx is returned. |
| A video job belongs to the account that created it. | A create answers the account id in two headers, and a poll pins that account and never rotates. |
| A Veo job id is a resource path, not a flat id. | It is base64url-encoded on the way out and decoded only when it re-encodes byte for byte and matches the anchored operation shape. |
| A search provider is the model, and its failure is not the account's. | The route resolves the provider from the body and locks `websearch:<id>` / `webfetch:<id>`, so a spent search key leaves chat working. |
| A vendor fetches the target from its own network, where our dialer never runs. | The target passes `network.CheckPublic` before any provider is called: scheme, host, and every resolved address. |
| The provider's `searchViaChat.defaultModel` is the search model, not a client's. | The adapter fills the address's `{model}` from the entry, so a client cannot point a chat search at another model. |

