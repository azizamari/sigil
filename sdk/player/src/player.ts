import Hls from "hls.js";
import { Overlay, type OverlayOptions } from "./overlay.js";

/** Private playlist tag carrying the base64 overlay string. */
export const OVERLAY_TAG = "#EXT-X-SIGIL-OVERLAY:";

export interface SigilPlayerOptions {
  playlistUrl: string;
  overlay?: OverlayOptions | false;
  /** Overrides the overlay text instead of reading it from the playlist. */
  overlayText?: string;
  /** Posted to when playback events occur. Omit to disable reporting. */
  eventsUrl?: string;
  sessionId?: string;
  sessionToken?: string;
  heartbeatMs?: number;
  hlsConfig?: Partial<Hls["config"]>;
  onError?: (error: SigilError) => void;
}

export interface SigilError {
  kind: "expired" | "forbidden" | "network" | "media" | "unsupported";
  message: string;
  fatal: boolean;
}

export type SigilEventType = "start" | "seek" | "heartbeat" | "complete";

/**
 * Wraps hls.js and renders the session overlay.
 *
 * There is no token juggling here: the playlist URL is already session-scoped
 * and expiring, and the segment URLs inside it are pre-signed, so the player
 * fetches media straight from the CDN and never calls sigil again.
 */
export class SigilPlayer {
  private readonly video: HTMLVideoElement;
  private readonly opts: SigilPlayerOptions;
  private hls: Hls | null = null;
  private overlay: Overlay | null = null;
  private heartbeat: ReturnType<typeof setInterval> | null = null;
  private started = false;
  private destroyed = false;

  constructor(video: HTMLVideoElement, options: SigilPlayerOptions) {
    this.video = video;
    this.opts = options;

    if (options.overlay !== false) {
      const host = video.parentElement ?? video;
      this.overlay = new Overlay(host as HTMLElement, options.overlay ?? {});
      if (options.overlayText) {
        this.overlay.setText(options.overlayText);
        this.overlay.start();
      }
    }

    this.attachVideoListeners();
    this.load();
    if (!options.overlayText) void this.loadOverlayText();
  }

  private load(): void {
    if (Hls.isSupported()) {
      this.loadWithHlsJs();
      return;
    }
    // Safari plays HLS natively and hls.js reports itself unsupported there.
    if (this.video.canPlayType("application/vnd.apple.mpegurl")) {
      this.video.src = this.opts.playlistUrl;
      return;
    }
    this.fail({ kind: "unsupported", message: "HLS is not supported in this browser", fatal: true });
  }

  private loadWithHlsJs(): void {
    const hls = new Hls({ enableWorker: true, ...(this.opts.hlsConfig ?? {}) });
    this.hls = hls;

    hls.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) return;
      this.fail(classify(data));
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        hls.recoverMediaError();
      }
    });

    hls.loadSource(this.opts.playlistUrl);
    hls.attachMedia(this.video);
  }

  private attachVideoListeners(): void {
    this.video.addEventListener("playing", () => {
      if (!this.started) {
        this.started = true;
        void this.report("start");
        this.startHeartbeat();
      }
    });
    // A seek repaints the frame the overlay sat on, so it is redrawn at a new
    // position rather than left to wait out its interval.
    this.video.addEventListener("seeked", () => {
      this.overlay?.show();
      void this.report("seek");
    });
    this.video.addEventListener("ended", () => {
      this.stopHeartbeat();
      void this.report("complete");
    });
  }

  private startHeartbeat(): void {
    const period = this.opts.heartbeatMs ?? 30000;
    if (!this.opts.eventsUrl || period <= 0) return;
    this.heartbeat = setInterval(() => void this.report("heartbeat"), period);
  }

  private stopHeartbeat(): void {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = null;
  }

  private async report(type: SigilEventType): Promise<void> {
    const { eventsUrl, sessionId, sessionToken } = this.opts;
    if (!eventsUrl || !sessionId || !sessionToken) return;
    try {
      await fetch(eventsUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          session_id: sessionId,
          token: sessionToken,
          type,
          position: this.video.currentTime,
        }),
        keepalive: true,
      });
    } catch {
      // Analytics must never interrupt playback.
    }
  }

  private async loadOverlayText(): Promise<void> {
    if (!this.overlay) return;
    try {
      const resp = await fetch(this.opts.playlistUrl);
      if (!resp.ok) return;
      const text = parseOverlayText(await resp.text());
      if (text && !this.destroyed) {
        this.overlay.setText(text);
        this.overlay.start();
      }
    } catch {
      // A missing overlay must never stop playback.
    }
  }

  private fail(error: SigilError): void {
    this.opts.onError?.(error);
  }

  get currentTime(): number {
    return this.video.currentTime;
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.stopHeartbeat();
    this.overlay?.destroy();
    this.hls?.destroy();
    this.hls = null;
  }
}

/**
 * Reads the overlay from the playlist itself. The tag is private, so hls.js
 * hands no parsed value back and the text is pulled from the raw playlist. The
 * playlist is a few kilobytes and marked no-store, so this is a cheap request.
 */
export function parseOverlayText(playlist: string): string {
  for (const line of playlist.split(/\r?\n/)) {
    if (!line.startsWith(OVERLAY_TAG)) continue;
    const encoded = line.slice(OVERLAY_TAG.length).trim();
    try {
      const binary = atob(encoded);
      const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
      return new TextDecoder().decode(bytes);
    } catch {
      return "";
    }
  }
  return "";
}

/**
 * An expired playlist must surface as a clear error rather than a silent hang,
 * because it is the failure a viewer hits after leaving a tab open too long.
 */
export function classify(data: { type: string; details?: string; response?: { code?: number } }): SigilError {
  const status = data.response?.code;
  if (status === 410) {
    return { kind: "expired", message: "This viewing session has expired. Reload to continue.", fatal: true };
  }
  if (status === 401 || status === 403) {
    return { kind: "forbidden", message: "This viewing session is not valid.", fatal: true };
  }
  if (data.type === "mediaError") {
    return { kind: "media", message: data.details ?? "Media error", fatal: true };
  }
  return { kind: "network", message: data.details ?? "Network error", fatal: true };
}
