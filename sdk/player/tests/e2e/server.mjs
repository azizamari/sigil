// Stands in for sigil serve plus the bucket: emits interleaved playlists and
// records which variant of each segment the player actually fetched.
import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { buildFixtures, FIXTURE_DIR, SEGMENT_COUNT, SEGMENT_SECONDS } from "./fixtures.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const PORT = Number(process.env.PORT ?? 8099);

buildFixtures();

/** @type {string[]} */
const fetched = [];

function playlist(sequence, overlay) {
  const lines = [
    "#EXTM3U",
    "#EXT-X-VERSION:3",
    `#EXT-X-TARGETDURATION:${SEGMENT_SECONDS}`,
    "#EXT-X-MEDIA-SEQUENCE:0",
    "#EXT-X-PLAYLIST-TYPE:VOD",
  ];
  if (overlay) {
    lines.push(`#EXT-X-SIGIL-OVERLAY:${Buffer.from(overlay, "utf8").toString("base64")}`);
  }
  for (let i = 0; i < SEGMENT_COUNT; i++) {
    const variant = sequence[i % sequence.length] === "1" ? 1 : 0;
    lines.push(`#EXTINF:${SEGMENT_SECONDS}.000,`);
    lines.push(`/v${variant}/seg_${String(i).padStart(5, "0")}.ts`);
  }
  lines.push("#EXT-X-ENDLIST");
  return lines.join("\n") + "\n";
}

function serveFile(res, path, type) {
  if (!existsSync(path) || !statSync(path).isFile()) {
    res.writeHead(404).end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": type, "Access-Control-Allow-Origin": "*" });
  createReadStream(path).pipe(res);
}

const server = createServer((req, res) => {
  const url = new URL(req.url ?? "/", `http://localhost:${PORT}`);
  const path = normalize(url.pathname);

  if (path === "/" || path === "/page.html") {
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end(readFileSync(join(here, "page.html")));
    return;
  }
  if (path === "/player.js") {
    serveFile(res, join(FIXTURE_DIR, "player.js"), "text/javascript");
    return;
  }
  if (path === "/playlist.m3u8") {
    const sequence = url.searchParams.get("seq") ?? "0";
    res.writeHead(200, {
      "Content-Type": "application/vnd.apple.mpegurl",
      "Cache-Control": "no-store",
      "Access-Control-Allow-Origin": "*",
    });
    res.end(playlist(sequence, url.searchParams.get("overlay")));
    return;
  }
  if (path === "/expired.m3u8") {
    res.writeHead(410, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" });
    res.end('{"error":"session: token has expired"}');
    return;
  }
  if (path === "/__fetched") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(fetched));
    return;
  }
  if (path === "/__reset") {
    fetched.length = 0;
    res.writeHead(204).end();
    return;
  }
  if (path === "/events") {
    res.writeHead(204, { "Access-Control-Allow-Origin": "*" }).end();
    return;
  }

  const segment = /^\/v([01])\/(seg_\d{5}\.ts)$/.exec(path);
  if (segment) {
    fetched.push(path);
    serveFile(res, join(FIXTURE_DIR, `v${segment[1]}`, segment[2]), "video/mp2t");
    return;
  }
  res.writeHead(404).end("not found");
});

server.listen(PORT, () => console.log(`harness listening on http://localhost:${PORT}`));
