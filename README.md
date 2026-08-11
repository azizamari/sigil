# sigil

A per-viewer forensic watermarking and signed-access layer for HLS, sitting in
front of any S3-compatible bucket.

sigil issues short-lived, session-bound playlists whose segment URLs are
pre-signed, and — once packaging lands — gives every viewer a slightly different
sequence of segment variants, so a leaked copy can be traced back to the session
it was issued to.

## What this is not

Read this section before anything else. Half the value of this project is that
it refuses to overclaim.

- **Not DRM.** No Widevine, PlayReady or FairPlay. No license server, no key
  escrow.
- **Not copy prevention.** sigil does not stop anyone recording or
  redistributing a video. It makes redistribution *attributable*.
- **No "anti-recording".** Screen capture cannot be blocked from a browser.
  Anything claiming otherwise is wrong.
- **Not a transcoder, CDN, or origin server.** v1 assumes you already have an
  encoded rendition and serves it from your bucket with signed URLs. A CDN in
  front is not optional.
- **Not collusion resistant.** Two viewers who compare their copies can see
  where they differ and splice around the mark. Defeating that needs Tardos
  codes, which are on the roadmap and not built.

The visible overlay is a deterrent, not a control. It is rendered client-side
and anyone who opens DevTools can remove it. That is by design: the overlay
discourages casual sharing, and the invisible A/B watermark is what survives a
technical user.

## Status

Early. The pieces below are built and tested; the rest of the roadmap is not.

| Component | State |
|---|---|
| `codebook` — sequence assignment, BCH ECC, soft decoding | done |
| `signer` — S3 pre-signed URLs | done |
| `storage` — S3-compatible client, asset layout | done |
| `manifest` — per-session HLS playlists | done |
| `serve` — session minting, playlist and event API | done |
| Player SDK and overlay | done |
| `embed` + `pack` — A/B variant packaging | done |
| `detect` — leak attribution | not started |

Until `detect` lands, sigil delivers signed, expiring, session-scoped HLS and
packages assets into two watermarked variants, but cannot yet attribute a leak
back to a session.

## Quick start

```bash
export SIGIL_API_KEY=$(openssl rand -base64 24)
export SIGIL_SESSION_KEY=$(sigil serve --print-key)

sigil serve \
  --base-url https://sigil.example.com \
  --s3-bucket my-course-videos \
  --s3-endpoint https://s3.example.com \
  --s3-region us-east-1 \
  --s3-path-style \
  --allowed-origins https://app.example.com
```

Register an asset you have already segmented into the bucket, then mint a
session from your backend:

```bash
curl -X POST https://sigil.example.com/v1/assets \
  -H "Authorization: Bearer $SIGIL_API_KEY" \
  -d '{"asset_id":"lecture-01","segment_count":300,"segment_duration_seconds":4}'

curl -X POST https://sigil.example.com/v1/sessions \
  -H "Authorization: Bearer $SIGIL_API_KEY" \
  -d '{"asset_id":"lecture-01","overlay_text":"viewer@example.com","ttl":3600}'
```

The response carries a `playlist_url`. Hand it to your frontend; the player
fetches segments straight from your bucket and never contacts sigil again.

## Credentials

There is no "us". sigil is a binary you run inside your own infrastructure.
Credentials are read from the AWS default chain — instance role first,
environment variables second, shared config last — and never transit anywhere
else. **No sigil API field anywhere accepts a secret**, in a body, a header or a
query parameter.

Give the serving key `s3:GetObject` on `assets/*` and nothing more. A
pre-signed URL carries whatever authority the signing key holds, so a key with
write access would let anyone holding a leaked playlist do more than read.

Keep the bucket private. A publicly readable bucket silently defeats the entire
session-scoping design.

## Overlay text is your decision

`overlay_text` is an arbitrary string. sigil renders it and does not parse,
validate or attach meaning to it. It is sealed into the playlist token with
authenticated encryption and **never stored**: sigil keeps session ids and
assigned sequences, never viewer identities. Resolve a session id back to a
person through your own database.

Putting an email on screen exposes it during a legitimate screen share. That
trade-off is yours to make.

## Segment duration affects detection

This is not obvious and it is easy to get wrong. Recovering an identifier from a
short clip needs enough segments in that clip to carry a full error-corrected
codeword with margin. For 10,000 distinguishable sessions recovered from 90
seconds of leaked content:

| segment duration | segments in 90s | result |
|---|---|---|
| 4s | 22 | no code fits |
| 2s | 45 | no code fits |
| 1s | 90 | no code fits, three segments short |
| 0.75s | 120 | works, 65,536 identities |

Shorter segments mean more requests and more container overhead. `codebook.Fit`
refuses to return parameters that cannot deliver a confident match rather than
silently returning something undetectable.

## Licence

Apache-2.0. Watermarking is a patent-dense field; the patent grant matters here.
This is not legal advice, and you should not build a commercial product on it
without counsel.
