# sigil — technical design spec

**Status:** Draft v0.1
**Working name:** `sigil` (alternatives: `hlsmark`, `traceline`, `plume`)
**License:** Apache-2.0 (patent grant matters here; watermarking is a patent-dense field)

A per-viewer forensic watermarking and signed-access layer for HLS, sitting in front of any S3-compatible bucket. Go core, thin Python and TypeScript SDKs, browser player SDK.

---

## 1. Scope

### In scope

- Signed, short-lived, session-bound HLS manifests and segment URLs.
- Per-session visible overlay watermark (viewer identifier), rendered by the player SDK.
- Per-session invisible A/B segment watermark, embedded server-side at packaging time.
- A detector that takes a leaked video file and returns the session it was issued to.
- Session event logging (start, seek, heartbeat, complete) for basic analytics.

### Explicitly not in scope

State these in the README, not just here. Half the credibility of this project comes from refusing to overclaim.

- **Not DRM.** No Widevine, PlayReady or FairPlay. No license server. No key escrow.
- **Not copy prevention.** This does not stop anyone from recording or redistributing. It makes redistribution attributable.
- **No "anti-recording."** Screen capture cannot be blocked from a browser. Any project claiming otherwise is wrong, and saying so plainly in the README is a differentiator.
- Not a transcoder. Assumes you already have an ABR ladder, or shells out to FFmpeg for a single rendition in the reference packager.
- Not a CDN, not an origin server for production scale (v1 serves from the bucket with signed URLs).

### Success criterion for v1.0

Given a video re-encoded at 60% bitrate, scaled to 720p, and cropped 5%, the detector identifies the issuing session with >95% accuracy from 90 seconds of content, across a population of 10,000 issued sessions.

That number is the whole project. Everything else is plumbing.

---

## 2. Threat model

Design against named adversary tiers. Each defense catches a specific tier and nothing beyond it.

| Tier | Adversary | Behaviour | Defense that catches it |
|---|---|---|---|
| T0 | Casual sharer | Pulls the MP4 or manifest URL, reposts the link | Signed, short-lived, session-bound URLs |
| T1 | Screen recorder | Records playback, uploads the file | Visible overlay (deters), A/B watermark (attributes) |
| T2 | Technical user | Opens DevTools, removes the overlay DOM node, records | A/B watermark only. Overlay is defeated here by design |
| T3 | Re-encoder | Transcodes, crops, scales, changes framerate | A/B watermark, if embedding is robust |
| T4 | Colluders | Two or more users average or splice their copies | Tardos-style codebook (v0.4, out of scope for v1) |
| T5 | Camera-on-screen | Films the monitor | Nothing reliable. Document this honestly |

The interesting engineering sits at T1 to T3. T0 is table stakes. T4 is a research problem. T5 is unsolved industry-wide.

---

## 3. Architecture

Four components, deliberately separable. A user should be able to adopt any one of them alone.

```
  ┌─────────────┐
  │  sigil pack │  offline CLI: ingest → segment → produce A/B variants
  └──────┬──────┘
         │ writes
         ▼
  ┌─────────────────────────────────────┐
  │   S3-compatible bucket              │
  │   assets/{id}/v0/seg_00001.ts       │
  │   assets/{id}/v1/seg_00001.ts       │
  └──────┬──────────────────────────────┘
         │ reads
         ▼
  ┌─────────────┐        ┌──────────────────┐
  │ sigil serve │◄───────│  your backend    │
  │  (Go)       │  mint  │  (py/ts SDK)     │
  └──────┬──────┘ session└──────────────────┘
         │ personalized manifest + signed URLs
         ▼
  ┌─────────────┐
  │ player SDK  │  hls.js + custom loader + overlay renderer
  └─────────────┘

  ┌─────────────┐
  │sigil detect │  leaked file → aligned bit sequence → session id
  └─────────────┘
```

### Why the manifest is the personalization point

This is the central design decision and worth writing down explicitly.

The per-viewer artifact is the **playlist**, which is a few kilobytes of text. The segments themselves are shared: there are exactly two variants of each segment for the entire user population, regardless of whether you have 10 viewers or 10 million.

Consequences:

- Storage and CDN cache footprint is 2x, not Nx. This is what makes A/B viable at all.
- Playlists are uncacheable and must be generated per session. They are cheap.
- CDN cache hit ratio drops relative to unmarked content, because requests split across two variants. Budget for it.
- The origin does no per-request video processing. Manifest generation is string assembly.

---

## 4. Milestones

Ship these as separate releases. Each is independently useful, which means each is independently launchable.

### v0.1 — Signed access + visible overlay

The weekend build. No FFmpeg, no watermark embedding.

- `sigil serve` issues session tokens (JWT, short TTL, bound to asset + session id + optional client fingerprint).
- Manifest endpoint returns a standard HLS playlist with signed segment URLs.
- Segment URLs are HMAC-signed with independent TTL, so a leaked playlist expires quickly.
- Player SDK wraps hls.js with a custom loader that attaches the session token.
- Overlay renderer draws the viewer identifier over the video: randomized position, configurable opacity and interval.
- Session events posted to a webhook.

Be honest in the docs that the overlay is a deterrent, not a control. It survives T1 and dies at T2.

### v0.2 — A/B packaging and detection

The real project.

- `sigil pack` takes a source file, segments it, and produces two watermarked variants per segment.
- Session assignment maps a session id to a bit sequence; the manifest interleaves variants accordingly.
- `sigil detect` takes a leaked file and recovers the sequence.

### v0.3 — Robustness

- Survive re-encode, scale, crop, framerate change.
- Confidence scoring on detection, not a bare answer.
- Partial-content detection (identify from a 90 second clip, not the full asset).

### v0.4 — Collusion resistance

Tardos codes. Research-grade. Do not promise this on the launch README.

---

## 5. Watermark embedding

Two candidate approaches. Pick pixel-domain for v1.

### Option A: pixel-domain luma modulation (recommended for v1)

Apply a low-amplitude luma perturbation over a fixed block grid, different between variant 0 and variant 1. Implementable as an FFmpeg filtergraph, so no encoder patching.

| Dimension | Assessment |
|---|---|
| Complexity | Low. FFmpeg filter chain plus a Go wrapper |
| Robustness | Moderate. Survives re-encode and scaling if amplitude and block size are tuned |
| Visual impact | Tunable. Needs perceptual testing |
| Bitrate cost | Some. Perturbation is new detail the encoder must spend bits on |

**Pros:** shippable in days, no custom encoder, easy to test.
**Cons:** measurable rate-distortion penalty, weaker against aggressive re-encoding.

### Option B: QP-domain / compression-domain embedding

Embed by varying quantization parameters during encoding. This is the approach in the academic literature (Mareen et al. and related work on rate-distortion-preserving A/B watermarking) precisely because it avoids the bitrate penalty.

| Dimension | Assessment |
|---|---|
| Complexity | High. Requires encoder-level control, x264/x265 zones or a patched encoder |
| Robustness | Higher |
| Visual impact | Lower |
| Bitrate cost | Near zero, which is the point |

**Decision:** Option A for v1. Structure the code so the embedder is an interface with one method, so Option B can land later without touching the packager, manifest server, or detector.

```go
type Embedder interface {
    // Embed writes a watermarked copy of src to dst carrying the given variant bit.
    Embed(ctx context.Context, src, dst string, variant uint8, params EmbedParams) error
    // Extract returns a soft-decision confidence in [-1, 1] that the input carries variant 1.
    Extract(ctx context.Context, src string, params EmbedParams) (float64, error)
}
```

Note that `Extract` returns a soft decision, not a bit. Soft decisions are what make sequence-level detection work under noise; a hard threshold per segment throws away information you need later.

---

## 6. Codebook and sequence assignment

### v1: direct binary assignment

Session id maps to an N-bit sequence where N is the segment count. With 4 second segments, a 20 minute video gives 300 segments, so 300 bits, which is far more capacity than needed for identity.

Use the surplus for redundancy, not for a larger address space:

- Allocate the first k bits to the session identifier.
- Repeat the identifier across the sequence, or apply a repetition or BCH code.
- Redundancy is what buys you partial-content detection: a 90 second clip is roughly 22 segments, and you need the identifier to be fully recoverable from any such window.

This is the design detail people will get wrong. Do not lay the id out once across the whole asset.

### Not in v1: collusion resistance

Two colluders comparing copies can see where they differ and splice. Tardos codes handle this by drawing bit probabilities from a non-uniform distribution so that some positions carry more statistical weight, making a colluder's spliced output still implicate a contributor. Note it in the roadmap, do not build it yet, and say clearly in the README that v1 is not collusion resistant.

---

## 7. Storage layout

```
assets/{asset_id}/
  manifest.base.m3u8      # template, variant placeholders
  meta.json               # segment count, duration, embed params, codebook version
  v0/seg_00001.ts ...
  v1/seg_00001.ts ...
```

Access via `aws-sdk-go-v2` with a custom endpoint, which covers AWS S3, MinIO, Backblaze B2, Cloudflare R2, Wasabi and Ceph. Do not write an abstraction over multiple SDKs; the S3 API is the abstraction.

Keep `meta.json` versioned. When you change embed parameters or the codebook in v0.3, previously packaged assets must still be detectable. A version field costs nothing now and saves the project later.

---

## 8. How sigil is used

### 8.1 What sigil is not in the path of

sigil does **not** proxy video. There is no media server and no persistent connection. HLS is plain HTTP GETs of small files, with the player handling sequencing, buffering and bitrate switching. The bucket or CDN serving `.ts` files over HTTP is the streaming server.

Consequently, pre-signed segment URLs are **baked directly into the personalized playlist**. The player fetches segments straight from the CDN or bucket and never contacts sigil again.

| | Requests to sigil | Requests to CDN / bucket |
|---|---|---|
| Per viewing session | ~1 (playlist) + occasional event posts | all segments |
| 20 min lecture, 4s segments | ~1 | ~300 |

An earlier draft of this spec had a `GET /v1/segment/{session}/{n}` endpoint returning a 302. That was wrong: it inserts sigil into every segment fetch, adding a round trip before any bytes arrive and turning a stateless text service into a bandwidth-adjacent one. Signing happens once, at playlist generation.

A single small VM therefore serves thousands of concurrent viewers, because it emits a few kilobytes of text per session and gets out of the way.

### 8.2 Load and cost, honestly

Request volume is not the concern. S3-compatible stores are built for exactly this access pattern.

**Egress bytes are the cost.** As an order of magnitude, 720p at roughly 2.5 Mbps is about 1 GB per viewer-hour, so a thousand viewers watching an hour is roughly a terabyte. Whether that is trivial or painful depends entirely on which S3-compatible store the user picked, since egress pricing varies enormously between AWS S3 and stores like Cloudflare R2 or Backblaze B2 whose pitch is a different egress model. Verify current pricing rather than trusting these figures; the shape is what matters. **A CDN in front is not optional.**

Because users will be behind a CDN, the URL signer must be pluggable. S3 pre-signed URLs, CloudFront signed URLs and Bunny token auth are three incompatible schemes.

```go
type URLSigner interface {
    Sign(key string, ttl time.Duration) (string, error)
}
```

**Cache behaviour.** Two costs, often confused:

- *Storage 2x is irrelevant.* A 40-hour course at 1 GB/hour becomes 80 GB. Nobody will notice.
- *Cache hit ratio is the real price of A/B.* Requests split across two variant prefixes, so the CDN holds two copies of hot content and warms up half as fast. State this plainly in the docs.

Headers: playlists are per session and must be `Cache-Control: no-store`. Segments never change and should be immutable with long TTLs.

### 8.3 HTTP API

Small enough to reimplement in an afternoon. That is what keeps the SDKs thin.

```
POST /v1/assets                    → { asset_id, status }
GET  /v1/assets/{id}               → { status, duration, segments }
POST /v1/sessions                  → { session_id, playlist_url, expires_at }
GET  /v1/playlist/{session_id}     → application/vnd.apple.mpegurl
POST /v1/events                    → 204
POST /v1/detect                    → { session_id, confidence, bits_recovered }
```

Session creation is server-to-server. The viewer's identity comes from the integrator's backend, never from the browser.

### 8.4 Integration walkthrough

Five steps. This is the section that belongs near the top of the README.

**1. Run it.** Single Go binary or a container. Point it at a bucket.

```bash
sigil serve \
  --s3-endpoint https://s3.example.com \
  --s3-bucket my-course-videos \
  --signer s3-presign \
  --api-key $SIGIL_API_KEY
```

**2. Package each video once, offline.** Produces both variants and uploads them.

```bash
sigil pack lecture-01.mp4 --asset-id lecture-01 --segment-duration 4
```

This is a batch job, not a request path. Wire it to whatever triggers on upload.

**3. Mint a session from your backend** when a logged-in viewer opens a lesson.

```python
client = sigil.Client(base_url, api_key)
session = client.create_session(
    asset_id="lecture-01",
    overlay_text=f"{user.email} · {order_id}",   # opaque to sigil
    ttl=3600,
)
return {"playlist_url": session.playlist_url}
```

`overlay_text` is an arbitrary string. sigil renders it and does not parse, validate or attach meaning to it. What goes in it is the integrator's decision, including the privacy consequences of putting an email on screen during a legitimate screen share.

**4. Play it in the frontend.**

```ts
const player = new SigilPlayer(videoEl, {
  playlistUrl,
  overlay: { opacity: 0.12, intervalMs: 20000, durationMs: 4000 },
});
```

The player SDK wraps hls.js and adds the overlay renderer. No token juggling in the browser: the playlist URL is already scoped and expiring.

**5. Investigate a leak** when one surfaces.

```bash
sigil detect leaked.mp4 --asset-id lecture-01
# session_id: ses_8f2a...  confidence: 0.97  bits_recovered: 46/48
```

Resolve the session id back to an account through your own database. sigil does not store viewer identities.

### 8.5 Adoption paths

Each component works alone, which widens the addressable set well beyond people willing to self-host a full video stack.

| The user wants | They adopt |
|---|---|
| Only expiring, session-scoped links | `sigil serve`, no packing, no watermark |
| Only a visible deterrent overlay | Player SDK alone, against their existing HLS |
| Attribution for leaks | `sigil pack` + `serve` + `detect` |
| Attribution on someone else's platform | `sigil pack` writing to their bucket, played by their own player |

### 8.7 Credentials and bucket configuration

There is no "us." sigil is a binary the operator runs inside their own infrastructure; credentials are read from their environment and never transit to a third party. Say this early in the README, because it is the main trust advantage over hosted platforms and it is the first thing a security reviewer will check.

**Credential sources, in order of preference.** Use the AWS SDK's default credential chain rather than inventing configuration. `config.LoadDefaultConfig` already resolves ambient identity, environment variables and shared config files in the right order.

1. **Ambient workload identity** (EC2 instance role, ECS task role, EKS IRSA, GKE workload identity). No static keys anywhere. This is the recommended path and should be the first example in the docs.
2. **Environment variables** (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`). The fallback for MinIO, R2, B2 and anyone not on AWS.
3. **Shared config file** for local development.

Never accept credentials over the HTTP API, in a query parameter, or in a request body. sigil's API surface should have no field anywhere that takes a secret.

**Non-AWS endpoints** need three extra knobs, and omitting any of them is the most common integration failure:

```
--s3-endpoint      https://<account>.r2.cloudflarestorage.com
--s3-region        auto          # R2 uses "auto"; B2 and MinIO differ
--s3-path-style    true          # required by MinIO; false for R2
```

**Least privilege: three roles, not one.**

| Component | Permissions needed | Notes |
|---|---|---|
| `sigil pack` | `s3:PutObject` on `assets/*` | Batch job. Can hold a separate, write-only key |
| `sigil serve` | `s3:GetObject` on `assets/*` | Read-only. See below |
| `sigil detect` | none | Operates on a local file |

A property worth documenting explicitly: **generating an S3 pre-signed URL is a purely local cryptographic operation.** It makes no network call and performs no permission check at signing time. The signature simply carries whatever authority the signing key holds. Two consequences follow. First, `sigil serve` can sign at high volume with zero S3 API cost or latency. Second, the signing key's policy *is* the access control, so scope it to `GetObject` on the asset prefix and nothing more. A signing key with write access would let anyone holding a leaked playlist URL do more than read.

**Bucket policy.** Keep the bucket entirely private. No public read, no public list. The pre-signed URL is the access mechanism, and a publicly readable bucket silently defeats the entire session-scoping design.

**CORS is required and is the most common setup failure.** The browser fetches segments cross-origin, and HLS seeking issues range requests. The bucket needs at minimum:

- Allowed methods: `GET`, `HEAD`
- Allowed origins: the integrator's app origins, not `*`
- Allowed headers: `Range`
- Exposed headers: `Content-Range`, `Content-Length`, `Accept-Ranges`

Omitting `Range` produces playback that starts fine and then breaks on seek, which is a confusing failure mode. Ship a `sigil doctor` command that fetches a test object with a range request and reports exactly what is misconfigured. It costs an hour and will prevent most of the project's support burden.

**CDN signing keys** are a separate concern from bucket credentials. CloudFront signed URLs use a dedicated key pair, and Bunny token auth uses a shared secret; neither is an S3 credential. Keep signer configuration in its own config block so the two are not conflated.

**Multi-tenant deployments.** If an operator runs one sigil instance across several customers' buckets, credentials become per-asset rather than global. Keep this out of v1, but make `storage.Client` resolvable per asset rather than a package-level singleton so it can be added later without a rewrite.

### 8.8 SDK scope

Python and TypeScript SDKs wrap session minting and asset management only. Roughly 200 lines each; they are not where the interesting code lives. The browser player SDK is the one that needs real care, because it is what people evaluate first.

---

## 9. Security considerations

- **Playlist URL is the credential.** It is session-scoped and expiring, and it embeds pre-signed segment URLs. Treat leaking it as equivalent to leaking access for its TTL.
- **TTL trade-off.** Segment URL TTL must outlast the longest plausible viewing session, or playback breaks mid-video for slow watchers. That means a scraped playlist stays usable for that window. Mitigate by keeping playlist TTL short (minutes) while segment TTL covers the asset duration plus margin, and by re-minting on resume rather than issuing multi-hour links.
- **Overlay is client-side.** Say so. It dies at T2. Do not let users believe it is enforced.
- **Overlay text is opaque and unvalidated.** sigil renders whatever string it is given. Document that putting an email on screen exposes it during legitimate screen shares, then leave the choice to the integrator.
- **Do not store overlay text.** Log the session id and the assigned sequence; let the integrator resolve identity from their own database. Storing the rendered string turns sigil into a PII store nobody intended to build.
- **Detection is evidence, not proof.** Return a confidence score alongside a documented false-positive rate. Anyone using this to accuse a person needs to understand the error rate. Put this in the README, not a footnote.
- **Timing side channel.** Playlist generation time must not vary with the bit sequence.

---

## 10. Testing strategy

The usual pyramid is inverted here. Most of this project's risk sits in one place: does detection actually identify the right session under real-world degradation. That question is not answerable by a unit test, so the suite has four layers with different rules.

```
   ┌──────────────────────────────────┐
   │  L4  Detection eval  (nightly)   │  statistical, thresholded
   ├──────────────────────────────────┤
   │  L3  E2E browser     (per PR)    │  Playwright + real hls.js
   ├──────────────────────────────────┤
   │  L2  Integration     (per PR)    │  MinIO + FFmpeg containers
   ├──────────────────────────────────┤
   │  L1  Unit            (per commit)│  pure, fast, no I/O
   └──────────────────────────────────┘
```

### L1 — Unit

Deterministic, no FFmpeg, no network, whole layer under 5 seconds.

| Area | What to assert |
|---|---|
| `codebook` | id → sequence → id round-trips; ECC recovers the id with up to *r* bit flips; any 90s window of the sequence contains a full recoverable id |
| `codebook` (property-based) | for random ids and random flip patterns within the correction bound, decode always returns the original |
| `manifest` | golden-file playlist output; generated playlists parse as valid HLS; variant interleaving matches the assigned sequence exactly |
| `signer` | URL structure, expiry encoding, tampered signature rejected, TTL boundaries |
| `detect` (decoding only) | given a synthetic soft-decision vector with injected noise, the decoder picks the right session |

That last row is the important one. Separate *bit recovery* from *signal extraction*. Feeding the decoder synthetic confidence vectors tests all the sequence logic in milliseconds, with no video involved. Only extraction needs real pixels.

### L2 — Integration

Real dependencies in containers. MinIO via testcontainers, pinned FFmpeg image.

- `pack` writes both variant prefixes with the expected object layout and a valid `meta.json`.
- `serve` mints a session, generates a playlist, and every embedded pre-signed URL resolves against MinIO.
- Expired URLs are rejected; tampered URLs are rejected.
- Playlist is `no-store`; segments carry long-lived cache headers.
- `sigil doctor` correctly diagnoses a deliberately broken CORS config.
- Provider matrix: MinIO in CI. Others tested manually and recorded in the support table.

**Pin the FFmpeg version in the container.** Encoder output is not bit-reproducible across builds, so byte-level golden files on encoded video will fail spuriously. Compare perceptual metrics or decoded frames, never bytes.

### L3 — E2E browser

Playwright against headless Chromium, one short fixture.

- Playback starts and reaches the end.
- **Seeking works.** This is the test that catches the missing CORS `Range` header, which is the failure mode that starts fine and breaks later.
- Overlay renders, respects opacity and interval config, and reappears after seek.
- Expired playlist produces a clean error rather than a hang.
- The player requests the correct variant per segment index for the session's sequence.

### L4 — Detection eval

**This is not a test suite. It is a benchmark, and it must be built differently.**

A test that asserts "detection succeeded" is the wrong shape. Detection is statistical, so assertions are thresholds over many trials with fixed seeds:

```
assert tpr(attack="reencode_60", clip_len=90) >= 0.95
assert fpr(threshold=0.9) <= 0.001
```

**Both directions matter, and the second is the one people skip.**

- *True positive rate:* watermarked leaks are attributed to the right session.
- *False positive rate:* run detection against **sessions that were never issued**, and against **unwatermarked content**, and confirm no match above threshold. Without this, your matcher will happily return a best-scoring candidate for any input, because with 10,000 issued sequences there is always a closest one. You need a null distribution and a confidence threshold derived from it. This is what makes "sigil says it was user X" defensible rather than an accusation generator.

Attack grid, run per clip length (30s / 60s / 90s / full):

| Attack | Implementation |
|---|---|
| Re-encode | FFmpeg at 40%, 60%, 80% of source bitrate |
| Scale | 1080p → 720p → 480p |
| Crop | 2%, 5%, 10% border removal |
| Framerate | 30 → 24 fps |
| Partial | random windows |
| Combined | re-encode + scale + crop |
| Negative | unissued sequences, unwatermarked source |
| Collusion (expected fail) | two sessions averaged or spliced |

Keep the collusion row and let it fail. An expected-failure test is executable documentation of the v1 limitation, and it stops anyone from quietly assuming collusion resistance exists.

**Fixtures must vary by content type, and this matters more than it looks.** Watermark robustness depends heavily on texture and motion:

- Talking head (moderate motion, skin tones)
- High motion (fast cuts, camera movement)
- **Screencast / slide deck** (near-static, flat colour, sharp text)

The third is the hard case, and it is also the dominant content type in e-learning, which is your target use case. Static flat regions give a pixel-domain watermark almost nowhere to hide, and the encoder allocates very few bits there. If robustness collapses on screencasts, the product does not work for its primary audience. Test it from day one, not after launch.

**Cadence and gating.** L4 is too slow for per-PR. Run it nightly against a committed baseline JSON, and fail the build if accuracy regresses more than a fixed margin. Small fixture subset can run per-PR as a smoke check.

**The output table is your README**, and the honest answer to the first question anyone will ask, which is whether this actually works.

---

## 11. Repo layout

```
cmd/
  sigil/            # CLI: pack, serve, detect
internal/
  embed/            # Embedder interface + ffmpeg pixel-domain impl
  codebook/         # sequence assignment, ECC
  manifest/         # HLS playlist generation
  storage/          # S3-compatible client
  signer/           # URLSigner impls: s3-presign, cloudfront, bunny
  detect/           # alignment + soft-decision decoding
  server/           # HTTP handlers
sdk/
  python/
  typescript/
  player/           # browser player SDK
testdata/
  attacks/          # attack simulator
docs/               # docs site
```

Go module at root. SDKs published separately to PyPI and npm. Player SDK is its own npm package; do not bundle it with the API client.

---

## 12. Open questions

Resolve these before or during v0.2, not before starting.

1. **Segment alignment in the detector.** A leaked file has no segment boundaries. Options: correlate a known sync pattern, use scene-change detection, or brute-force offset search. This is the hardest unsolved piece of v0.2 and worth prototyping first, before the packager.
2. **Amplitude tuning.** How strong can the perturbation be before it is visible, and how weak before it dies under re-encode? Needs an actual perceptual test with real people, not a PSNR number.
3. **ETSI TS 104 002 conformance.** The DASH-IF forensic A/B watermarking spec was republished by ETSI in 2023 and defines interoperable interfaces for HLS and DASH. Decide early whether to target conformance or just be inspired by it. Conformance is a strong marketing claim and a constraint on your interfaces. Read the spec before locking the API.
4. **Live streaming.** Out of scope for v1. Do not let it influence the design; retrofitting VOD to live is normal, and designing for both from day one will stall the project.
5. **Multi-rendition ABR.** v1 assumes one rendition. Real ABR means watermarking every rung of the ladder consistently so that a mid-playback bitrate switch does not corrupt the sequence. Non-trivial. Plan for it in the meta format, build it in v0.3.

---

## 13. Risks

- **Alignment may be harder than expected.** If you cannot reliably locate segment boundaries in a re-encoded leak, the whole A/B approach stalls. Prototype detection first, on hand-crafted watermarked files, before building any packaging or serving code. If it does not work, you have lost two days rather than three weeks.
- **Patent exposure.** Traitor tracing and A/B watermarking have significant patent activity. Apache-2.0 gives contributors a patent grant but does not protect you from third parties. Do not build a commercial product on this without counsel.
- **Scope creep toward the full platform.** Every user will ask for transcoding, a management UI, and analytics. Say no in the README before they ask.
- **Accusation risk.** Someone will use this to accuse a student of leaking a course. Publish your false-positive rate prominently and frame the output as evidence with a confidence score.
