# CLAUDE.md — sigil

Read `SPEC.md` before writing any code. It is the source of truth for scope, architecture, and non-goals. If something you are about to build contradicts the spec, stop and say so rather than resolving it yourself.

---

## Working agreement

**Build incrementally.** One feature at a time, in the order given below. Do not scaffold ahead. Do not create empty packages or placeholder files for work in later phases.

**Test as you go.** Every phase ships with its tests written in the same phase, not retrofitted later. A phase is not done until its tests pass.

**Keep going.** After merging a phase to `main`, report what landed and continue straight into the next phase. Do not wait for confirmation between phases.

**Ask before deviating.** Continuing is the default, but it is not a licence to improvise. Stop and ask if you hit any of these:

- A phase needs a dependency, an architecture change, or anything not in the spec.
- A test fails and the fix is not obvious within a couple of attempts.
- Phase 1's spike result is negative, or ambiguous.
- Something you are about to build contradicts `SPEC.md`.

Never skip a phase, reorder phases, or mark a phase done with failing tests in order to keep moving.

**Small steps inside a phase.** Within a phase, work in commit-sized units. Write the code, run the tests, commit, move on. Do not write 800 lines and then test.

---

## Git workflow

- Branch per feature: `feat/<short-name>`, `fix/<short-name>`, `chore/<short-name>`.
- Never commit directly to `main`.
- Merge to `main` only when the feature is functional and its tests pass.
- Use `--no-ff` merges so feature boundaries stay visible in history.
- Delete the branch after merging.

```bash
git checkout -b feat/codebook
# ... work, test, commit in small units ...
git checkout main
git merge --no-ff feat/codebook
git branch -d feat/codebook
```

## Commit messages

**Title only. No body. Ever.**

- Conventional-commit prefix: `feat:`, `fix:`, `test:`, `refactor:`, `docs:`, `chore:`, `ci:`.
- Imperative mood, lowercase after the prefix, under 72 characters.
- No bullet lists, no explanation paragraphs, no "Summary" or "Test plan" sections.
- **No `Co-Authored-By` trailer. No "Generated with Claude Code" line. No AI attribution of any kind, in commits or PR descriptions.**

Good:
```
feat: add BCH encoding to codebook
test: cover signer TTL boundaries
fix: reject tampered segment signatures
```

Bad:
```
feat: add BCH encoding to codebook

- Implements BCH(63,45) over GF(2^6)
- Adds round-trip tests

Co-Authored-By: Claude <noreply@anthropic.com>
```

---

## Build order

Follow this sequence. The ordering is deliberate: it front-loads the risk.

### Phase 0 — Skeleton
Go module, directory layout per SPEC section 11, MIT/Apache license file, `.gitignore`, GitHub Actions running `go vet`, `go test ./...`, and `golangci-lint`. Nothing else.

### Phase 1 — Detection feasibility spike
**Do this before anything else, and treat it as throwaway.**

Hand-craft two watermarked variants of a short clip with an FFmpeg filter. Re-encode at 60% bitrate. Write the crudest possible extractor and see whether variant 0 and variant 1 are distinguishable at all.

Report the result and wait for a decision before proceeding. This is the one hard stop in the build. If the signal does not survive a basic re-encode, the whole A/B approach needs rethinking, and it is far better to learn that in a day than in three weeks. Commit the spike on a branch, do not merge it, and do not build on its code.

### Phase 2 — `codebook` (no video, no I/O)
Sequence assignment, ECC, decoding from soft-decision vectors. Pure Go, fully unit-tested, property-based tests for the ECC round trip. This is the highest-value-per-line package in the project and it can be perfected without touching a single video file.

### Phase 3 — `signer` and `storage`
`URLSigner` interface with the S3 pre-sign implementation. S3-compatible client via `aws-sdk-go-v2`. Integration tests against MinIO in testcontainers.

### Phase 4 — `manifest` and `serve` (v0.1, no watermark)
Playlist generation with embedded pre-signed URLs, session minting, event endpoint. This is a shippable release on its own: signed, expiring, session-scoped HLS. Tag it `v0.1.0`.

### Phase 5 — Player SDK and overlay
TypeScript, wraps hls.js. Overlay renderer takes an opaque string. Playwright E2E, including the seek test.

### Phase 6 — `embed` and `pack`
`Embedder` interface plus the FFmpeg pixel-domain implementation. Segmenting, dual-variant encoding, upload, `meta.json`.

### Phase 7 — `detect`
Segment alignment, soft-decision extraction, sequence matching against issued sessions. Confidence scoring with a null distribution.

### Phase 8 — Eval harness
Attack grid, fixture set (talking head, high motion, screencast), true-positive and false-positive measurement, baseline JSON, nightly workflow. Tag `v0.2.0` when the numbers are real.

### Phase 9 — SDKs and docs
Python and TypeScript API clients from an OpenAPI spec. Docs site.

---

## Testing rules

Four layers, per SPEC section 10. Do not blur them.

| Layer | Speed | When it runs | Rule |
|---|---|---|---|
| L1 unit | <5s total | every commit | No I/O, no FFmpeg, no network, no containers |
| L2 integration | seconds | every PR | testcontainers MinIO, pinned FFmpeg image |
| L3 E2E | ~1 min | every PR | Playwright, one short fixture |
| L4 eval | minutes+ | nightly | Statistical thresholds, not booleans |

Specific requirements:

- **Never assert byte equality on encoded video.** FFmpeg output is not reproducible across builds. Compare decoded frames or perceptual metrics.
- **Test the decoder with synthetic soft-decision vectors**, not real video. Bit recovery and signal extraction are separate concerns and must be tested separately.
- **Write the negative detection tests.** Detection must be run against unissued sequences and unwatermarked content and must return no match. Skipping this produces a detector that always names someone.
- **Keep the collusion test as an expected failure.** It documents the v1 limitation.
- Eval assertions are thresholds with fixed seeds: `assert tpr(attack, clip_len) >= 0.95`, not `assert detected == true`.

---

## Go conventions

- Standard library first. Justify every new dependency in the phase report before adding it.
- Interfaces defined by the consumer, kept small. `Embedder`, `URLSigner`, `storage.Client`.
- `context.Context` as the first parameter on anything doing I/O.
- Errors wrapped with `%w` and context; no bare `err` returns from deep call stacks.
- No package-level mutable state. `storage.Client` in particular must be resolvable per asset, not a singleton, so multi-tenant support can land later without a rewrite.
- Table-driven tests.
- `golangci-lint` clean before every commit.

---

## Do not

- Do not add live streaming, a transcoding ladder, DRM, an admin UI, or user management. All are explicit non-goals in SPEC section 1.
- Do not add a `GET /v1/segment/...` endpoint. Pre-signed URLs go in the playlist. See SPEC 8.1.
- Do not accept credentials over the HTTP API, in query parameters, or in request bodies.
- Do not parse, validate, or interpret `overlay_text`. It is an opaque string.
- Do not store `overlay_text` anywhere. Log session ids only.
- Do not write claims about preventing recording, screen capture, or piracy in any code comment, doc, or README line.
- Do not generate SDK clients with a code generator. Six endpoints; hand-write them.
- Do not add cgo, WASM, or Go-to-Python bindings. SDKs talk HTTP.
- Do not commit fixture videos larger than a few MB. Generate synthetic fixtures with FFmpeg where possible.

---

## Reporting

At the end of each phase, report in this format, then continue to the next phase:

```
Phase N — <name>
Merged: feat/<branch> → main
Commits: <count>
Tests: <passing>/<total>, new coverage areas
Deviations from spec: <none | description>
Open questions: <none | list>
```

The one exception is Phase 1. Report the spike result and wait, because a negative result invalidates the phases that follow it.
