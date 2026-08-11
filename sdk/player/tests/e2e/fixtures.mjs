// Generates the media and page bundle the browser tests need. Fixtures are
// synthesised rather than committed so the repo carries no binary video.
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
export const FIXTURE_DIR = join(here, ".fixtures");
export const SEGMENT_SECONDS = 1;
export const SEGMENT_COUNT = 8;

function ffmpeg(args) {
  execFileSync("ffmpeg", ["-y", "-hide_banner", "-loglevel", "error", ...args], { stdio: "pipe" });
}

// The two variants differ by a visible luma offset. Real packaging uses a
// low-amplitude block pattern; here the only thing under test is that the
// player fetches the variant the playlist names.
function buildVariant(dir, variant) {
  mkdirSync(dir, { recursive: true });
  const tint = variant === 0 ? "0.0" : "0.25";
  ffmpeg([
    "-f", "lavfi",
    "-i", `testsrc2=size=320x180:rate=15:duration=${SEGMENT_COUNT * SEGMENT_SECONDS}`,
    "-vf", `eq=brightness=${tint}`,
    "-c:v", "libx264", "-preset", "ultrafast", "-g", String(15 * SEGMENT_SECONDS),
    "-keyint_min", String(15 * SEGMENT_SECONDS), "-sc_threshold", "0",
    "-pix_fmt", "yuv420p",
    "-f", "hls",
    "-hls_time", String(SEGMENT_SECONDS),
    "-hls_playlist_type", "vod",
    "-hls_segment_filename", join(dir, "seg_%05d.ts"),
    join(dir, "index.m3u8"),
  ]);
}

export function buildFixtures() {
  if (existsSync(join(FIXTURE_DIR, "v1", "seg_00000.ts")) && existsSync(join(FIXTURE_DIR, "player.js"))) {
    return;
  }
  rmSync(FIXTURE_DIR, { recursive: true, force: true });
  mkdirSync(FIXTURE_DIR, { recursive: true });

  buildVariant(join(FIXTURE_DIR, "v0"), 0);
  buildVariant(join(FIXTURE_DIR, "v1"), 1);

  execFileSync("npx", [
    "esbuild", join(here, "harness.ts"),
    "--bundle", "--format=esm", "--platform=browser",
    `--outfile=${join(FIXTURE_DIR, "player.js")}`,
  ], { stdio: "pipe", cwd: join(here, "..", "..") });
}

if (import.meta.url === `file://${process.argv[1]}`) {
  buildFixtures();
  console.log("fixtures ready in", FIXTURE_DIR);
}
