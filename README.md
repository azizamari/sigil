# sigil

A per-viewer forensic watermarking and signed-access layer for HLS, in front of
any S3-compatible bucket.

sigil issues short-lived, session-bound playlists with pre-signed segment URLs,
and gives every viewer a slightly different sequence of segment variants, so a
leaked copy can be traced back to the session it was issued to.

## What this is not

- **Not DRM.** No Widevine, PlayReady or FairPlay. No license server.
- **Not copy prevention.** It does not stop anyone recording or redistributing.
  It makes redistribution attributable.
- **No "anti-recording".** Screen capture cannot be blocked from a browser.
- **Not a transcoder or CDN.** v1 serves an existing rendition from your bucket.
  A CDN in front is not optional.
- **Not collusion resistant.** Two viewers who compare copies can splice around
  the mark. Tardos codes are roadmap, not built.

The visible overlay is a deterrent, not a control. It lives in the DOM and
anyone who opens DevTools can delete it. The invisible segment watermark is what
survives that.

## Status

| Component | State |
|---|---|
| `codebook` sequence assignment, BCH ECC, soft decoding | done |
| `signer` S3 pre-signed URLs | done |
| `storage` S3-compatible client, asset layout | done |
| `manifest` per-session HLS playlists | done |
| `serve` session minting, playlist and event API | done |
| Player SDK and overlay | done |
| `embed` + `pack` A/B variant packaging | done |
| `detect` leak attribution | done |
| Published accuracy numbers | in progress |

## Quick start

```bash
export SIGIL_API_KEY=$(openssl rand -base64 24)
export SIGIL_SESSION_KEY=$(sigil serve --print-key)

sigil pack lecture-01.mp4 --asset-id lecture-01 --s3-bucket my-videos

sigil serve \
  --base-url https://sigil.example.com \
  --s3-bucket my-videos \
  --allowed-origins https://app.example.com
```

Mint a session from your backend and hand the `playlist_url` to your frontend:

```bash
curl -X POST https://sigil.example.com/v1/sessions \
  -H "Authorization: Bearer $SIGIL_API_KEY" \
  -d '{"asset_id":"lecture-01","overlay_text":"viewer@example.com","ttl":3600}'
```

The player fetches segments straight from your bucket and never contacts sigil
again. Investigate a leak with:

```bash
sigil detect leaked.mp4 --asset-id lecture-01 --sessions issued.json
```

## Credentials

sigil is a binary you run in your own infrastructure. Credentials come from the
AWS default chain and never transit anywhere else. No API field anywhere accepts
a secret.

Give the serving key `s3:GetObject` on `assets/*` and nothing more. A pre-signed
URL carries whatever authority the signing key holds. Keep the bucket private; a
publicly readable bucket defeats the entire session-scoping design.

## Overlay text is your decision

`overlay_text` is an arbitrary string. sigil renders it and does not parse or
validate it. It is sealed into the playlist token with authenticated encryption
and never stored. sigil keeps session ids and assigned sequences, never viewer
identities.

Putting an email on screen exposes it during a legitimate screen share. That
trade-off is yours.

## Detection is evidence, not proof

`sigil detect` returns a confidence and the peak score reached by a sequence
that was never issued. Read them together. A match counts only when it clears
both the threshold and that null peak.

Two behaviours stop this being an accusation generator:

- Unwatermarked content produces no attribution, rather than naming whichever
  session sits closest to the noise.
- A genuine leak whose session is not in the issued list produces no match, even
  at high confidence, rather than blaming the nearest innocent viewer.

Anyone using this to accuse a person needs to understand the error rate behind
the number. Publish yours. Run `sigil eval` to measure it.

## Segment duration affects detection

Recovering an identifier from a short clip needs enough segments in that clip to
carry a full error-corrected codeword with margin. For 10,000 sessions
recovered from 90 seconds:

| segment duration | segments in 90s | result |
|---|---|---|
| 4s | 22 | no code fits |
| 2s | 45 | no code fits |
| 1s | 90 | three segments short |
| 0.75s | 120 | works, 65,536 identities |

Shorter segments mean more requests. `sigil pack --dry-run` reports the plan and
refuses configurations that cannot deliver a confident match.

## Licence

Apache-2.0. Watermarking is a patent-dense field and the patent grant matters.
This is not legal advice; do not build a commercial product on it without
counsel.
