// Test page entrypoint: exposes the SDK on window so specs can drive it.
import { SigilPlayer } from "../../src/index.js";
import type { SigilError } from "../../src/index.js";

declare global {
  interface Window {
    sigil: {
      create: (opts: Record<string, unknown>) => void;
      errors: SigilError[];
      events: string[];
      player?: SigilPlayer;
    };
  }
}

const errors: SigilError[] = [];
const events: string[] = [];

window.sigil = {
  errors,
  events,
  create(opts) {
    const video = document.querySelector("video") as HTMLVideoElement;
    window.sigil.player = new SigilPlayer(video, {
      playlistUrl: String(opts.playlistUrl),
      overlay: (opts.overlay as never) ?? {},
      onError: (e) => errors.push(e),
      ...(opts.extra as object),
    });
    for (const type of ["playing", "seeked", "ended"]) {
      video.addEventListener(type, () => events.push(type));
    }
  },
};
