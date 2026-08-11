import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import { Overlay, OVERLAY_DEFAULTS } from "../src/overlay.js";
import { classify, parseOverlayText, OVERLAY_TAG } from "../src/player.js";

function container(): HTMLElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientWidth", { value: 640, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: 360, configurable: true });
  document.body.appendChild(el);
  return el;
}

describe("Overlay", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = "";
  });
  afterEach(() => vi.useRealTimers());

  it("renders nothing until it has text", () => {
    const o = new Overlay(container());
    o.start();
    expect(o.visible).toBe(false);
  });

  it("becomes visible at the configured opacity once started", () => {
    const o = new Overlay(container(), { opacity: 0.2 });
    o.setText("viewer@example.com");
    o.start();
    expect(o.visible).toBe(true);
    expect(o.element.style.opacity).toBe("0.2");
  });

  it("hides after durationMs and returns after intervalMs", () => {
    const o = new Overlay(container(), { durationMs: 1000, intervalMs: 5000, opacity: 0.5 });
    o.setText("mark");
    o.start();
    expect(o.visible).toBe(true);

    vi.advanceTimersByTime(1000);
    expect(o.visible).toBe(false);

    vi.advanceTimersByTime(5000);
    expect(o.visible).toBe(true);
  });

  it("moves between appearances so it cannot be cropped out once", () => {
    const o = new Overlay(container(), { durationMs: 10, intervalMs: 10 });
    o.setText("mark");
    o.start();

    const seen = new Set<string>();
    for (let i = 0; i < 25; i++) {
      seen.add(`${o.element.style.left},${o.element.style.top}`);
      vi.advanceTimersByTime(20);
    }
    expect(seen.size).toBeGreaterThan(1);
  });

  it("keeps the mark inside the frame", () => {
    const o = new Overlay(container());
    o.setText("mark");
    o.start();
    for (let i = 0; i < 200; i++) {
      const left = Number.parseFloat(o.element.style.left);
      const top = Number.parseFloat(o.element.style.top);
      expect(left).toBeGreaterThanOrEqual(0);
      expect(left).toBeLessThanOrEqual(100);
      expect(top).toBeGreaterThanOrEqual(0);
      expect(top).toBeLessThanOrEqual(100);
      o.show();
    }
  });

  // Overlay text is an arbitrary integrator-supplied string. It is set with
  // textContent precisely so it cannot become markup.
  it("treats the text as opaque and never as markup", () => {
    const host = container();
    const o = new Overlay(host);
    o.setText('<img src=x onerror="globalThis.pwned = true">');
    o.start();

    expect(host.querySelector("img")).toBeNull();
    expect((globalThis as Record<string, unknown>).pwned).toBeUndefined();
    expect(o.element.textContent).toContain("<img");
  });

  it("does not interpret the text as anything meaningful", () => {
    const o = new Overlay(container());
    for (const text of ["viewer@example.com · order-42", "", "   ", "日本語のテキスト", "a".repeat(400)]) {
      o.setText(text);
      expect(o.element.textContent).toBe(text);
    }
  });

  it("stops and cleans up on destroy", () => {
    const host = container();
    const o = new Overlay(host);
    o.setText("mark");
    o.start();
    o.destroy();

    expect(host.querySelector("[data-sigil-overlay]")).toBeNull();
    vi.advanceTimersByTime(60000);
    expect(o.visible).toBe(false);
  });

  it("gives the container a positioning context so the mark lands over the video", () => {
    const host = container();
    new Overlay(host);
    expect(host.style.position).toBe("relative");
  });

  it("uses documented defaults", () => {
    expect(OVERLAY_DEFAULTS.opacity).toBe(0.12);
    expect(OVERLAY_DEFAULTS.intervalMs).toBe(20000);
    expect(OVERLAY_DEFAULTS.durationMs).toBe(4000);
  });
});

describe("classify", () => {
  it("maps an expired playlist to a clear, actionable error", () => {
    const err = classify({ type: "networkError", details: "manifestLoadError", response: { code: 410 } });
    expect(err.kind).toBe("expired");
    expect(err.message).toMatch(/expired/i);
  });

  it("distinguishes forbidden from expired", () => {
    expect(classify({ type: "networkError", response: { code: 403 } }).kind).toBe("forbidden");
    expect(classify({ type: "networkError", response: { code: 401 } }).kind).toBe("forbidden");
  });

  it("falls back to network and media kinds", () => {
    expect(classify({ type: "networkError", details: "fragLoadError" }).kind).toBe("network");
    expect(classify({ type: "mediaError", details: "bufferStalledError" }).kind).toBe("media");
  });
});

describe("parseOverlayText", () => {
  const playlist = (line: string) => `#EXTM3U\n#EXT-X-VERSION:3\n${line}\n#EXTINF:4.000,\nseg.ts\n#EXT-X-ENDLIST\n`;

  it("decodes the private tag", () => {
    const text = "viewer@example.com · order-42";
    const encoded = Buffer.from(text, "utf8").toString("base64");
    expect(parseOverlayText(playlist(OVERLAY_TAG + encoded))).toBe(text);
  });

  it("returns empty when the tag is absent", () => {
    expect(parseOverlayText(playlist("#EXT-X-PLAYLIST-TYPE:VOD"))).toBe("");
    expect(parseOverlayText("")).toBe("");
  });

  it("survives a malformed payload rather than throwing", () => {
    expect(parseOverlayText(playlist(OVERLAY_TAG + "!!!not-base64!!!"))).toBe("");
  });

  it("round-trips non-ascii text", () => {
    for (const text of ["日本語のテキスト", "émoji 🎬 ok", "a".repeat(300)]) {
      const encoded = Buffer.from(text, "utf8").toString("base64");
      expect(parseOverlayText(playlist(OVERLAY_TAG + encoded))).toBe(text);
    }
  });

  // Base64 means arbitrary integrator text can never terminate the tag or add
  // a line to the playlist.
  it("cannot carry playlist syntax through the tag", () => {
    const hostile = '"\n#EXT-X-ENDLIST\n#EXTINF:1,\nhttps://attacker.example/x.ts';
    const encoded = Buffer.from(hostile, "utf8").toString("base64");
    const out = playlist(OVERLAY_TAG + encoded);
    expect(out.split("\n").filter((l) => l === "#EXT-X-ENDLIST")).toHaveLength(1);
    expect(parseOverlayText(out)).toBe(hostile);
  });
});
