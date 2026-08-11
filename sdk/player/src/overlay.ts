export interface OverlayOptions {
  /** Fraction of the container's smaller edge, used as the font size. */
  scale?: number;
  opacity?: number;
  /** Milliseconds between appearances. */
  intervalMs?: number;
  /** Milliseconds each appearance stays on screen. */
  durationMs?: number;
  className?: string;
}

export const OVERLAY_DEFAULTS: Required<Omit<OverlayOptions, "className">> = {
  scale: 0.028,
  opacity: 0.12,
  intervalMs: 20000,
  durationMs: 4000,
};

type Timer = ReturnType<typeof setTimeout>;

/**
 * Draws a viewer identifier over the video at randomised positions.
 *
 * This is a deterrent against casual re-sharing, not a control: it lives in the
 * DOM and anyone who opens developer tools can delete it. The invisible
 * segment watermark is what survives that.
 */
export class Overlay {
  private readonly container: HTMLElement;
  private readonly node: HTMLElement;
  private readonly opts: Required<Omit<OverlayOptions, "className">>;
  private showTimer: Timer | null = null;
  private hideTimer: Timer | null = null;
  private running = false;
  private text = "";

  constructor(container: HTMLElement, options: OverlayOptions = {}) {
    this.container = container;
    this.opts = {
      scale: options.scale ?? OVERLAY_DEFAULTS.scale,
      opacity: options.opacity ?? OVERLAY_DEFAULTS.opacity,
      intervalMs: options.intervalMs ?? OVERLAY_DEFAULTS.intervalMs,
      durationMs: options.durationMs ?? OVERLAY_DEFAULTS.durationMs,
    };

    const doc = container.ownerDocument;
    this.node = doc.createElement("div");
    this.node.dataset.sigilOverlay = "";
    if (options.className) this.node.className = options.className;
    Object.assign(this.node.style, {
      position: "absolute",
      pointerEvents: "none",
      userSelect: "none",
      whiteSpace: "pre",
      opacity: "0",
      color: "#ffffff",
      textShadow: "0 1px 2px rgba(0,0,0,0.6)",
      fontFamily: "system-ui, sans-serif",
      transition: "opacity 400ms linear",
      zIndex: "2147483647",
    } satisfies Partial<CSSStyleDeclaration>);

    const position = container.ownerDocument.defaultView?.getComputedStyle(container).position;
    if (!position || position === "static") {
      container.style.position = "relative";
    }
    container.appendChild(this.node);
  }

  /**
   * The string is set with textContent and never parsed: sigil attaches no
   * meaning to it, and it must not be able to inject markup.
   */
  setText(text: string): void {
    this.text = text;
    this.node.textContent = text;
  }

  start(): void {
    if (this.running || !this.text) return;
    this.running = true;
    this.show();
  }

  stop(): void {
    this.running = false;
    this.clearTimers();
    this.node.style.opacity = "0";
  }

  destroy(): void {
    this.stop();
    this.node.remove();
  }

  /** Exposed so the player can bring the overlay back after a seek. */
  show(): void {
    if (!this.running) return;
    this.clearTimers();
    this.reposition();
    this.node.style.opacity = String(this.opts.opacity);

    this.hideTimer = setTimeout(() => {
      this.node.style.opacity = "0";
      this.showTimer = setTimeout(() => this.show(), this.opts.intervalMs);
    }, this.opts.durationMs);
  }

  get element(): HTMLElement {
    return this.node;
  }

  get visible(): boolean {
    return Number(this.node.style.opacity) > 0;
  }

  private reposition(): void {
    const rect = this.container.getBoundingClientRect();
    const width = rect.width || this.container.clientWidth || 640;
    const height = rect.height || this.container.clientHeight || 360;

    this.node.style.fontSize = `${Math.max(10, Math.round(Math.min(width, height) * this.opts.scale))}px`;

    // Keep the mark inside the frame: a partially clipped overlay is both
    // easier to crop away and worse to look at.
    const margin = 0.06;
    const span = 1 - margin * 2;
    this.node.style.left = `${(margin + Math.random() * span) * 100}%`;
    this.node.style.top = `${(margin + Math.random() * span) * 100}%`;
    this.node.style.transform = "translate(-50%, -50%)";
  }

  private clearTimers(): void {
    if (this.showTimer) clearTimeout(this.showTimer);
    if (this.hideTimer) clearTimeout(this.hideTimer);
    this.showTimer = null;
    this.hideTimer = null;
  }
}
