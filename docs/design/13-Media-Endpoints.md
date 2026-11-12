# Design 13 — Media Endpoints (`app/gateway/`)

**Status:** partial

No media adapter exists. Each endpoint resolves model and account as chat does, then calls a per-provider adapter registered by provider id and modality. Usage rows carry the modality in `endpoint`.

| Endpoint | Notes |
|---|---|
| Embeddings | OpenAI shape; custom embedding nodes supported |
| Images | OpenAI-style and vendor adapters; image models never stream |
| Audio | Speech, transcriptions, voice lists |
| Video | Create, edit, extend, then poll by id |
| Search | Normalizes results across search vendors; SearXNG through `MEDIA_SEARXNG_URL` |
| Fetch | URL to markdown; every target goes through the SSRF guard |

---

## Remaining work

| Slice | Scope | Status |
|---|---|---|
| Foundations | Media schema, index, `media/mux`, `app/gateway/media`, embeddings, non-browser images, probes, one usage row per request | done |
| Vendor images | Stability, BFL, fal, HuggingFace, ComfyUI, SD WebUI, Runway, Cloudflare, NanoBanana, plus the codex and antigravity bridges | done |
| Audio | TTS, STT, voice lists: `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/voices`, the two dashboard routes | done |
| Video | `/v1/videos/generations\|edits\|extensions` and `GET /v1/videos/{id}` | todo |
| Search and fetch | `/v1/search` and `/v1/web/fetch`; the HTML-to-markdown library is an open decision; `MEDIA_SEARXNG_URL`; SSRF on every hop | todo |
| Browser and web media | chatgpt-web, grok-web, gemini-web, muse-web, uc-web, veoaifree-web, and anything the earlier slices defer | todo |

Bounds already built: a 25 MiB upload, a 32 KiB speech input, a 120 s speech timeout and a 300 s transcription timeout.

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
| 299 | The scraped and OS-spawned speech providers are out: `edge-tts` and `google-tts` screen-scrape Bing and Translate, and `local-device` runs the OS speech tool through ffmpeg. **Recorded ** | Page scraping and process spawns are outside the router's rules (`design doc 11`, `design doc 22`) |
| 300 | `aws-polly` is out: the reference has no handler for its format, so no behavior exists to copy. **Recorded ** | A dead entry is not a feature |
| 301 | Codex transcription takes only 16-bit PCM mono 24 kHz WAV and never converts; the reference runs ffmpeg for every format. **Built** | A spawned process is forbidden; a clear 400 beats a silent mismatch |
| 302 | A transcription upload is bounded at 25 MiB; the reference's route accepts the 128 MiB proxy cap. **Built** | One request cannot exhaust memory (`AGENTS.md`) |
| 303 | AssemblyAI gets the key raw in `Authorization`, as its API documents; the reference's own config falls through to `Bearer`. **Built** | The documented form |
| 304 | Speech is served as `audio/mpeg`; the reference writes the non-standard `audio/mp3`. **Built** | The registered media type |
| 305 | The reference's Gemini-native `/v1beta/models/…:generateContent` audio route is not built: the chat route owns that path. Gemini speech is served through `/v1/audio/speech`. **Deferred to ** | One path, one owner; the TTS adapter already reaches the same model |
| 298 | A no-auth service runs without a connection, and an existing connection is never reported or locked; the reference never touches credentials for these providers. **Built** | A local daemon's failure is not the account's, and a locked connection must not stop a service that needs none |

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
| Codex's native image endpoint accepts one shape only. | A tool model with a plain prompt, `n=1`, `size:auto` and a png; anything else is a 400. |
| A Free-plan account is refused before the call, from the token's own claim. | It is passed over without locking the account. |

