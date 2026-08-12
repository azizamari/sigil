# Operating it

## Credentials

Use the AWS default chain. In order of preference:

1. Ambient workload identity (instance role, task role, IRSA). No static keys.
2. Environment variables, for MinIO, R2, B2 and anything not on AWS.
3. Shared config file, for local development.

No sigil API field accepts a secret, in a body, a header or a query parameter.

## Least privilege

| Component | Permissions |
|---|---|
| `sigil pack` | `s3:PutObject` on `assets/*` |
| `sigil serve` | `s3:GetObject` on `assets/*` |
| `sigil detect` | none, it works on a local file |

Generating a pre-signed URL is a purely local cryptographic operation. It makes
no network call and performs no permission check, so the signing key's policy
*is* the access control. A key with write access would let anyone holding a
leaked playlist do more than read.

## Bucket policy

Keep the bucket entirely private. No public read, no public list. The
pre-signed URL is the access mechanism, and a publicly readable bucket silently
defeats the entire session-scoping design.

## CORS

Required, and the most common setup failure. At minimum:

- Methods: `GET`, `HEAD`
- Origins: your app origins, not `*`
- Allowed headers: `Range`
- Exposed headers: `Content-Range`, `Content-Length`, `Accept-Ranges`

Omitting `Range` produces playback that starts fine and breaks on seek.

## Non-AWS endpoints

Three extra knobs, and omitting any of them is the usual integration failure:

```
--s3-endpoint    https://<account>.r2.cloudflarestorage.com
--s3-region      auto      # R2 uses "auto"; B2 and MinIO differ
--s3-path-style  true      # required by MinIO, false for R2
```

## TTLs

Segment URLs must outlast the longest plausible viewing session or playback
breaks part way through. That means a scraped playlist stays usable for that
window. Keep the playlist token short-lived and re-mint on resume rather than
issuing multi-hour links.

## Caching

Playlists are per session and must be `no-store`. Segments never change and
should be immutable with long TTLs.

Cache hit ratio, not storage, is the real price of A/B: requests split across
two variant prefixes, so a CDN holds two copies of hot content and warms up half
as fast. Storage at 2x is irrelevant by comparison.

## Rotating the session key

Changing `SIGIL_SESSION_KEY` invalidates every outstanding playlist token at
once. That is the intended way to revoke access in bulk.

## Known operational gap

Issued sessions are held in memory. They are lost on restart, and detection
needs them to match a leak against what was actually handed out. Export them or
run a persistent store before relying on attribution in production.
