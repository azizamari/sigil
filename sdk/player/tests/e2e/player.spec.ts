import { expect, test, type Page } from "@playwright/test";

const SEGMENTS = 8;

async function load(page: Page, query: string, extra: Record<string, unknown> = {}) {
  await page.request.post("/__reset");
  await page.goto("/page.html");
  await page.waitForFunction(() => Boolean(window.sigil));
  await page.evaluate(
    ([q, e]) => window.sigil.create({ playlistUrl: `/playlist.m3u8?${q}`, extra: e }),
    [query, extra] as const,
  );
}

function video(page: Page) {
  return page.locator("video");
}

async function play(page: Page, rate = 8) {
  await page.evaluate((r) => {
    const v = document.querySelector("video") as HTMLVideoElement;
    v.playbackRate = r;
    return v.play();
  }, rate);
}

test("playback starts and reaches the end", async ({ page }) => {
  await load(page, "seq=01");
  await play(page);

  await expect
    .poll(() => page.evaluate(() => (document.querySelector("video") as HTMLVideoElement).currentTime), {
      timeout: 30_000,
    })
    .toBeGreaterThan(0.5);

  await page.waitForFunction(() => (document.querySelector("video") as HTMLVideoElement).ended, null, {
    timeout: 40_000,
  });
  expect(await page.evaluate(() => window.sigil.events)).toContain("ended");
});

// Seeking issues range requests. This is the failure that starts fine and
// breaks later, so it gets its own test.
test("seeking works and playback continues from the new position", async ({ page }) => {
  await load(page, "seq=01");
  await play(page, 1);
  await page.waitForFunction(() => (document.querySelector("video") as HTMLVideoElement).currentTime > 0.2);

  await page.evaluate(() => {
    (document.querySelector("video") as HTMLVideoElement).currentTime = 5;
  });
  await page.waitForFunction(() => window.sigil.events.includes("seeked"));

  const after = await page.evaluate(() => (document.querySelector("video") as HTMLVideoElement).currentTime);
  expect(after).toBeGreaterThan(4.5);

  await page.waitForFunction(() => (document.querySelector("video") as HTMLVideoElement).currentTime > 5.2, null, {
    timeout: 20_000,
  });
});

test("overlay renders the session text over the video", async ({ page }) => {
  await load(page, "seq=01&overlay=viewer%40example.com");
  await play(page);

  const overlay = page.locator("[data-sigil-overlay]");
  await expect(overlay).toHaveText("viewer@example.com");
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity))).toBeGreaterThan(0);
});

test("overlay respects the configured opacity and interval", async ({ page }) => {
  await load(page, "seq=01&overlay=mark", {
    overlay: { opacity: 0.42, intervalMs: 1000, durationMs: 500 },
  });
  await play(page);

  const overlay = page.locator("[data-sigil-overlay]");
  await expect.poll(() => overlay.evaluate((el) => el.style.opacity)).toBe("0.42");

  // It hides after durationMs...
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity)), { timeout: 10_000 }).toBe(0);
  // ...and comes back after intervalMs.
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity)), { timeout: 10_000 }).toBeGreaterThan(0);
});

test("overlay reappears after a seek", async ({ page }) => {
  await load(page, "seq=01&overlay=mark", {
    overlay: { opacity: 0.3, intervalMs: 60_000, durationMs: 300 },
  });
  await play(page, 1);

  const overlay = page.locator("[data-sigil-overlay]");
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity))).toBeGreaterThan(0);
  // With a 60s interval it will not return on its own within this test.
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity)), { timeout: 10_000 }).toBe(0);

  await page.evaluate(() => {
    (document.querySelector("video") as HTMLVideoElement).currentTime = 4;
  });
  await expect.poll(() => overlay.evaluate((el) => Number(el.style.opacity)), { timeout: 10_000 }).toBeGreaterThan(0);
});

// The overlay is a deterrent, not a control, and the E2E suite says so out loud.
test("overlay can be removed from the DOM, which is why it is not the real defence", async ({ page }) => {
  await load(page, "seq=01&overlay=mark");
  await play(page);
  await expect(page.locator("[data-sigil-overlay]")).toHaveCount(1);

  await page.evaluate(() => document.querySelector("[data-sigil-overlay]")?.remove());
  await expect(page.locator("[data-sigil-overlay]")).toHaveCount(0);

  await page.waitForFunction(() => (document.querySelector("video") as HTMLVideoElement).currentTime > 1);
});

test("an expired playlist reports a clean error instead of hanging", async ({ page }) => {
  await page.goto("/page.html");
  await page.waitForFunction(() => Boolean(window.sigil));
  await page.evaluate(() => window.sigil.create({ playlistUrl: "/expired.m3u8" }));

  await expect.poll(() => page.evaluate(() => window.sigil.errors.length), { timeout: 20_000 }).toBeGreaterThan(0);
  const first = await page.evaluate(() => window.sigil.errors[0]);
  expect(first.kind).toBe("expired");
  expect(first.message).toMatch(/expired/i);
});

// The variant fetched per segment index is the watermark. If the player
// silently substitutes the other copy, every attribution is wrong.
test("the player fetches the variant the playlist assigns", async ({ page }) => {
  const sequence = "10110";
  await load(page, `seq=${sequence}`);
  await play(page);
  await page.waitForFunction(() => (document.querySelector("video") as HTMLVideoElement).ended, null, {
    timeout: 40_000,
  });

  const fetched: string[] = await (await page.request.get("/__fetched")).json();
  for (let i = 0; i < SEGMENTS; i++) {
    const want = `/v${sequence[i % sequence.length]}/seg_${String(i).padStart(5, "0")}.ts`;
    expect(fetched, `segment ${i} must come from the assigned variant`).toContain(want);
  }

  const wrong = fetched.filter((path) => {
    const m = /^\/v([01])\/seg_(\d{5})\.ts$/.exec(path);
    if (!m) return false;
    return m[1] !== sequence[Number(m[2]) % sequence.length];
  });
  expect(wrong, "player fetched segments from the unassigned variant").toEqual([]);
});
