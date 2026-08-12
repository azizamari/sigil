/**
 * Thin client for the sigil HTTP API.
 *
 * Session creation is server to server. This client holds an API key, so it
 * belongs in your backend and must never be bundled into a page.
 */

export interface Asset {
  assetId: string;
  status: string;
  duration?: number;
  segments?: number;
  watermarked?: boolean;
}

export interface Session {
  sessionId: string;
  playlistUrl: string;
  expiresAt: string;
}

export class SigilError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(`sigil: ${status}: ${message}`);
    this.name = "SigilError";
    this.status = status;
  }
}

export interface ClientOptions {
  baseUrl: string;
  apiKey: string;
  fetch?: typeof globalThis.fetch;
  timeoutMs?: number;
}

export class Client {
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly doFetch: typeof globalThis.fetch;
  private readonly timeoutMs: number;

  constructor(options: ClientOptions) {
    if (!options.baseUrl) throw new Error("baseUrl is required");
    if (!options.apiKey) throw new Error("apiKey is required");
    this.baseUrl = options.baseUrl.replace(/\/+$/, "");
    this.apiKey = options.apiKey;
    this.doFetch = options.fetch ?? globalThis.fetch;
    this.timeoutMs = options.timeoutMs ?? 10_000;
  }

  async createAsset(input: {
    assetId: string;
    segmentCount: number;
    segmentDurationSeconds: number;
    totalDurationSeconds?: number;
  }): Promise<Asset> {
    const body: Record<string, unknown> = {
      asset_id: input.assetId,
      segment_count: input.segmentCount,
      segment_duration_seconds: input.segmentDurationSeconds,
    };
    if (input.totalDurationSeconds !== undefined) {
      body.total_duration_seconds = input.totalDurationSeconds;
    }
    const data = await this.request<{ asset_id: string; status: string }>("POST", "/v1/assets", body);
    return { assetId: data.asset_id, status: data.status };
  }

  async getAsset(assetId: string): Promise<Asset> {
    const data = await this.request<{
      asset_id: string;
      status: string;
      duration: number;
      segments: number;
      watermarked: boolean;
    }>("GET", `/v1/assets/${encodeURIComponent(assetId)}`);
    return {
      assetId: data.asset_id,
      status: data.status,
      duration: data.duration,
      segments: data.segments,
      watermarked: data.watermarked,
    };
  }

  /**
   * Mint a viewing session.
   *
   * overlayText is opaque to sigil: it is rendered and never stored or
   * interpreted. Putting an email on screen exposes it during a legitimate
   * screen share, which is your decision.
   */
  async createSession(input: {
    assetId: string;
    overlayText?: string;
    ttl?: number;
  }): Promise<Session> {
    const data = await this.request<{
      session_id: string;
      playlist_url: string;
      expires_at: string;
    }>("POST", "/v1/sessions", {
      asset_id: input.assetId,
      overlay_text: input.overlayText ?? "",
      ttl: input.ttl ?? 3600,
    });
    return {
      sessionId: data.session_id,
      playlistUrl: data.playlist_url,
      expiresAt: data.expires_at,
    };
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    let response: Response;
    try {
      response = await this.doFetch(`${this.baseUrl}${path}`, {
        method,
        headers: {
          Authorization: `Bearer ${this.apiKey}`,
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: controller.signal,
      });
    } catch (err) {
      throw new SigilError(0, err instanceof Error ? err.message : String(err));
    } finally {
      clearTimeout(timer);
    }

    const text = await response.text();
    if (!response.ok) {
      throw new SigilError(response.status, errorMessage(text));
    }
    return (text ? JSON.parse(text) : {}) as T;
  }
}

function errorMessage(text: string): string {
  try {
    const parsed = JSON.parse(text) as { error?: string };
    return parsed.error ?? text;
  } catch {
    return text;
  }
}
