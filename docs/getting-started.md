# Getting started

## 1. Package a video

```bash
sigil pack lecture-01.mp4 --asset-id lecture-01 --dry-run
```

`--dry-run` reports the plan without encoding. It tells you the segment count,
the watermark grid, and how many distinguishable sessions the chosen codebook
supports. If the configuration cannot deliver a confident match it fails here
rather than producing an undetectable asset.

Drop `--dry-run` and add your bucket flags to encode and upload both variants.

## 2. Serve it

```bash
export SIGIL_API_KEY=$(openssl rand -base64 24)
export SIGIL_SESSION_KEY=$(sigil serve --print-key)

sigil serve \
  --base-url https://sigil.example.com \
  --s3-bucket my-videos \
  --allowed-origins https://app.example.com
```

Secrets come from the environment, never flags, so they stay out of shell
history and process listings.

## 3. Mint a session from your backend

```python
import sigil

client = sigil.Client("https://sigil.example.com", api_key)
session = client.create_session(
    asset_id="lecture-01",
    overlay_text=f"{user.email} · {order_id}",
    ttl=3600,
)
return {"playlist_url": session.playlist_url}
```

The TypeScript client is equivalent:

```ts
const client = new Client({ baseUrl, apiKey });
const session = await client.createSession({ assetId: "lecture-01", overlayText: user.email });
```

Both hold an API key, so both belong on your server. Never ship either to a
browser.

## 4. Play it

```ts
const player = new SigilPlayer(videoEl, {
  playlistUrl,
  overlay: { opacity: 0.12, intervalMs: 20000, durationMs: 4000 },
});
```

No token juggling in the browser: the playlist URL is already scoped and
expiring, and the segment URLs inside it are pre-signed.

## 5. Investigate a leak

```bash
sigil detect leaked.mp4 --asset-id lecture-01 --sessions issued.json
```

`issued.json` is the list of sessions you handed out, which is what makes the
answer defensible. See [reading a detection result](detection.md).
