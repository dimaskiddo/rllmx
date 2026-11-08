# PRD 13 — Media Endpoints (`app/media/`)

**Status:** todo · **Steps:** 1.11

`app/media` does not exist. Each endpoint resolves model and account as chat does, then calls a per-provider adapter registered by provider id and modality. Usage rows carry the modality in `endpoint`.

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
**Step 1.11.** Nothing built.

- [ ] Embeddings (`/v1/embeddings`, custom embedding nodes)
- [ ] Images (`/v1/images/generations`; image models never stream)
- [ ] TTS (`/v1/audio/speech`, `/v1/audio/voices`), STT (`/v1/audio/transcriptions`, bounded uploads)
- [ ] Video (`/v1/videos/generations|edits|extensions`, `GET /v1/videos/{id}` polling)
- [ ] Search (`/v1/search`) and fetch (`/v1/web/fetch`, URL to markdown, SSRF guard every hop); the HTML-to-markdown library is an open decision
- [ ] Media probes and the TTS voice lists; `MEDIA_SEARXNG_URL` in `.env.example`
- [ ] Usage rows for media (modality is the `endpoint`)
- **Done when:** each endpoint passes its end-to-end test against fake providers; oversized uploads and SSRF targets are rejected.

---

## Divergences

Deliberate differences from the references in this area. Numbers are stable and cited from other docs.

| # | What we do differently | Why it is better |
|---|---|---|
| 137 | Gemini audio request (`responseModalities` AUDIO or `speechConfig`) to non-Gemini target: 400; only `image/*` blobs become images | No text where speech asked |
| 128 | Max 16 images, 20 MiB per request, 10 MiB each, duplicates fetched once; stops when cancelled **(enforced by `app/prefetch` since 1.8d)** | One request cannot exhaust memory |
| 161 | Moved to step 1.11 with media providers | Media providers, not chat providers (scope cut) |

---

## Findings

None recorded.
