# PRD 13 — Media Endpoints (`app/gateway/`)

**Status:** partial · **Steps:** 1.11a to 1.11e

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

**1.11a (done).** Schema, media index, gateway skeleton, embeddings, non-browser images, media probes.
Built: `Entry.Media` and its configs, the media model index, `app/llm/media` with openai/google/github/ollama/selfhosted, `media/mux`, `app/gateway/media`, `POST /v1/embeddings` and `POST /v1/images/generations`, one usage row per request.
Left:
- [ ] Every remaining media provider file (media blocks on the chat-plus-media entries, the media-only TTS/STT/search/fetch/video/web files)
- [ ] Vendor image adapters (stability, BFL, fal, HuggingFace, ComfyUI, SD WebUI, Runway, Cloudflare, NanoBanana) and the codex/antigravity bridges. Codex's own image path: the `gpt-image-2.6-flare` and `gpt-image-2.6-sunburst` models, `imageConfig`, native OAuth generation gated to a plain prompt, `reasoning` omitted, stream error surfaced (0.5.116)
- [ ] Media probes in the checker
- [ ] Embeddings input bounds (a divergence row) and the controller/route tests

**1.11b.** TTS (`/v1/audio/speech`), STT (`/v1/audio/transcriptions`, bounded uploads), voice lists (`/v1/audio/voices`, `/api/media-providers/tts/*/voices`).

**1.11c.** Video (`/v1/videos/generations|edits|extensions`, `GET /v1/videos/{id}` polling).

**1.11d.** Search (`/v1/search`) and fetch (`/v1/web/fetch`); the HTML-to-markdown library is an open decision; `MEDIA_SEARXNG_URL` in `.env.example`; SSRF on every hop.

**1.11e.** Browser/web media providers and whatever b–d defer.

- **Done when:** every endpoint passes its end-to-end test against fake providers; oversized uploads and SSRF targets are rejected.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 137 | Gemini audio request (`responseModalities` AUDIO or `speechConfig`) to non-Gemini target: 400; only `image/*` blobs become images | No text where speech asked |
| 128 | Max 16 images, 20 MiB per request, 10 MiB each, duplicates fetched once; stops when cancelled **(enforced by `app/gateway/prefetch` since 1.8d)** | One request cannot exhaust memory |
| 161 | Moved to step 1.11 with media providers | Media providers, not chat providers (scope cut) |
| 293 | Stability's generate request is **multipart/form-data**; the reference sends JSON, which v2beta rejects. **Built in 1.11a** | A request the vendor documents and accepts |
| 294 | fal's `image_size` is an object (`{width,height}`); the reference sends the `"16:9"` string, which fal rejects. **Built in 1.11a** | A request the vendor documents and accepts |
| 295 | Runway's `ratio` is a pixel ratio (`1024:1792`); the reference sends `1:1`, which Runway rejects. **Built in 1.11a** | A request the vendor documents and accepts |
| 296 | An Antigravity answer with no image is an error; the reference answers 200 with an empty `b64_json`. **Built in 1.11a** | A success with no image is not a success |
| 297 | An embeddings request is bounded (2,048 inputs, 32 KiB each); the reference accepts any size. **Built in 1.11a** | One request cannot exhaust memory (`AGENTS.md`) |
| 298 | A no-auth service runs without a connection, and an existing connection is never reported or locked; the reference never touches credentials for these providers. **Built in 1.11a** | A local daemon's failure is not the account's, and a locked connection must not stop a service that needs none |

---

## Findings

**1.11a: media is a second request path, not a second pipeline.** `gateway/media` reuses the selector, reporter, refresher, minter and usage writer, and adds only what a non-chat call needs: one body read, one adapter call, one row. The reference's media probe is a `POST` of `{"input":"ping","prompt":"ping"}` whose only bad answer is a refused key; ours keeps that rule so a vendor's 400 for a malformed ping cannot fail a good connection.

**A no-auth service is the operator's.** Its saved connection supplies the address, but the router never reports or locks it: a local daemon's 5xx is the daemon's, not the account's, and a locked connection must not stop a service that takes no credential (divergence 298).

**Vendor defects not copied.** Stability's content type, fal's `image_size`, Runway's `ratio` and antigravity's empty-image success are recorded as 293 to 296; the vendors' own documented shapes are sent instead. NanoBanana's misspelled `TEXTTOIAMGE`/`IMAGETOIAMGE` is kept verbatim, as the upstream expects it.

**Probe limits.** A media probe treats anything but a refused key as a good key, so a base path that answers 404 is read as valid — the reference's own weakness, kept because a vendor's ping status is not a stable contract. A live check settles it (NOTES 1.1).

**Codex image path.** Native generation only: a tool model, a plain prompt, `n=1`, `size:auto`, a png. Anything else is a 400 with a fixed word; the reference routes those to its Responses `image_generation` path, which waits for 1.11e. A Free-plan account is refused before the call, from the token's own claim.

