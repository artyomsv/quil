import { CLOSE } from './protocol';
import { sanitizeRemoteText } from './sanitize';

export interface BannerState {
  text: string;
  retrying: boolean;
}

// bannerFor says what the page shows after the socket closed with code. A
// resync (4001) reconnects at once and shows nothing; null means no banner.
// The reason comes from the gateway, which may relay the daemon's words, so it
// is sanitized like every other remote name.
export function bannerFor(code: number, reason: string, retrying: boolean): BannerState | null {
  const why = sanitizeRemoteText(reason);
  switch (code) {
    case CLOSE.resync:
      return null;
    case CLOSE.tooSlow:
      return { text: 'This tab fell behind — reconnecting', retrying };
    case CLOSE.daemonUnavailable:
      return { text: 'The daemon is unavailable — reconnecting', retrying };
    case CLOSE.tokenRefused:
      return { text: `Token refused: ${why}`, retrying: false };
    case CLOSE.versionMismatch:
      return { text: `Version mismatch: ${why}`, retrying: false };
    case CLOSE.byAgent:
      return { text: 'Closed by an agent', retrying: false };
    case CLOSE.goingAway:
      return { text: 'The web server stopped', retrying: false };
    default:
      return retrying
        ? { text: 'Connection lost — reconnecting', retrying: true }
        : { text: `Connection closed${why ? `: ${why}` : ''}`, retrying: false };
  }
}
