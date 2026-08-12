import { describe, expect, it, vi } from "vitest";
import { Client, SigilError } from "../src/index.js";

function stub(status: number, body: unknown) {
  return vi.fn(async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

const opts = { baseUrl: "https://sigil.example.com", apiKey: "test-key" };

describe("Client", () => {
  it("requires a base url and an api key", () => {
    expect(() => new Client({ baseUrl: "", apiKey: "k" })).toThrow();
    expect(() => new Client({ baseUrl: "https://x", apiKey: "" })).toThrow();
  });

  it("sends the api key as a bearer token", async () => {
    const fetch = stub(200, { asset_id: "lecture-01", status: "ready", duration: 60, segments: 80, watermarked: true });
    await new Client({ ...opts, fetch }).getAsset("lecture-01");

    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("https://sigil.example.com/v1/assets/lecture-01");
    expect((init!.headers as Record<string, string>).Authorization).toBe("Bearer test-key");
  });

  it("maps snake_case responses to camelCase", async () => {
    const fetch = stub(201, {
      session_id: "ses_abc",
      playlist_url: "https://sigil.example.com/v1/playlist/ses_abc?t=tok",
      expires_at: "2026-01-01T00:00:00Z",
    });
    const session = await new Client({ ...opts, fetch }).createSession({
      assetId: "lecture-01",
      overlayText: "viewer@example.com",
    });
    expect(session.sessionId).toBe("ses_abc");
    expect(session.expiresAt).toBe("2026-01-01T00:00:00Z");
  });

  it("defaults the ttl and overlay so a minimal call is valid", async () => {
    const fetch = stub(201, { session_id: "s", playlist_url: "u", expires_at: "e" });
    await new Client({ ...opts, fetch }).createSession({ assetId: "lecture-01" });
    const body = JSON.parse(fetch.mock.calls[0]![1]!.body as string);
    expect(body.ttl).toBe(3600);
    expect(body.overlay_text).toBe("");
  });

  it("surfaces the API error message and status", async () => {
    const fetch = stub(404, { error: "asset not found" });
    const client = new Client({ ...opts, fetch });
    await expect(client.getAsset("nope")).rejects.toThrow(SigilError);
    await expect(client.getAsset("nope")).rejects.toThrow(/asset not found/);
  });

  it("escapes asset ids in the path", async () => {
    const fetch = stub(200, { asset_id: "a b", status: "ready", duration: 0, segments: 0, watermarked: false });
    await new Client({ ...opts, fetch }).getAsset("a b");
    expect(fetch.mock.calls[0]![0]).toBe("https://sigil.example.com/v1/assets/a%20b");
  });

  it("strips a trailing slash from the base url", async () => {
    const fetch = stub(200, { asset_id: "x", status: "ready", duration: 0, segments: 0, watermarked: false });
    await new Client({ baseUrl: "https://sigil.example.com/", apiKey: "k", fetch }).getAsset("x");
    expect(fetch.mock.calls[0]![0]).toBe("https://sigil.example.com/v1/assets/x");
  });

  it("wraps transport failures rather than leaking them raw", async () => {
    const fetch = vi.fn(async () => {
      throw new Error("connection refused");
    });
    await expect(new Client({ ...opts, fetch }).getAsset("x")).rejects.toThrow(SigilError);
  });
});
