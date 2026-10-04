import { Terminal } from '@xterm/xterm';
import { WebglAddon } from '@xterm/addon-webgl';
import '@xterm/xterm/css/xterm.css';
import type { TermLike } from './terminals';

// The font size a terminal starts at, and the one a size master lays out at.
export const BASE_FONT = 14;

export interface XtermPane extends TermLike {
  // open() on first show, then move the element; adds the WebGL renderer.
  attach(el: HTMLElement): void;
  // Removes the WebGL renderer (browsers allow about 16 contexts).
  detach(): void;
  setFontSize(px: number): void;
  // Pixels per cell at the current font.
  measureCell(): { width: number; height: number };
  onData(fn: (s: string) => void): void;
  focus(): void;
  // The buffer as text, trailing blank lines removed (the end-to-end hook).
  text(): string;
}

// createXtermPane makes the terminal un-opened: writes before open() fill its
// buffer with no renderer, so a hidden pane costs memory and parsing only.
// open() runs the first time the pane is shown; later shows move its element.
// The WebGL renderer is attached only while visible, because browsers cap
// WebGL contexts at about 16; the DOM renderer covers context loss.
export function createXtermPane(_paneId: string): XtermPane {
  const term = new Terminal({ scrollback: 1000, allowProposedApi: false, fontSize: BASE_FONT, cursorBlink: false });
  let host: HTMLElement | null = null;
  let webgl: WebglAddon | null = null;
  let dataSub: { dispose(): void } | null = null;
  let disposed = false;
  return {
    write(data, done) {
      term.write(data, done);
    },
    reset() {
      term.reset();
    },
    resize(cols, rows) {
      if (cols > 0 && rows > 0) term.resize(cols, rows);
    },
    dispose() {
      disposed = true;
      dataSub?.dispose();
      dataSub = null;
      webgl?.dispose();
      webgl = null;
      term.dispose();
    },
    attach(el) {
      if (disposed) return;
      if (!host) {
        host = document.createElement('div');
        host.style.width = '100%';
        host.style.height = '100%';
        el.appendChild(host);
        term.open(host);
      } else if (host.parentElement !== el) {
        el.appendChild(host);
      }
      if (!webgl) {
        try {
          webgl = new WebglAddon();
          webgl.onContextLoss(() => {
            webgl?.dispose();
            webgl = null;
          });
          term.loadAddon(webgl);
        } catch {
          webgl = null; // the DOM renderer stays
        }
      }
    },
    detach() {
      webgl?.dispose();
      webgl = null;
    },
    setFontSize(px) {
      if (term.options.fontSize !== px) term.options.fontSize = px;
    },
    measureCell() {
      const el = host?.querySelector('.xterm-screen') as HTMLElement | null;
      if (!el || term.cols === 0 || term.rows === 0) return { width: 0, height: 0 };
      return { width: el.clientWidth / term.cols, height: el.clientHeight / term.rows };
    },
    // One listener at a time: a new one replaces the previous.
    onData(fn) {
      if (disposed) return;
      dataSub?.dispose();
      dataSub = term.onData(fn);
    },
    focus() {
      if (!disposed) term.focus();
    },
    text() {
      if (disposed) return '';
      const buf = term.buffer.active;
      const lines: string[] = [];
      for (let i = 0; i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? '');
      return lines.join('\n').trimEnd();
    },
  };
}
