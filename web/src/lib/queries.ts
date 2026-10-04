import type { IFunctionIdentifier, IParser } from '@xterm/xterm';

// The queries xterm.js answers by writing a reply into its own input, which
// the page would send to the pane as typed bytes:
//   CSI c, CSI > c          device attributes (DA1, DA2)
//   CSI n, CSI ? n          device status and cursor position (DSR, DECXCPR)
//   CSI $ p, CSI ? $ p      mode requests (DECRQM)
//   DCS $ q ... ST          setting requests (DECRQSS)
//   OSC 4/10/11/12 with ?   color queries
// Window reports (CSI t) stay off by xterm's own default.
const CSI_QUERIES: IFunctionIdentifier[] = [
  { final: 'c' },
  { prefix: '>', final: 'c' },
  { final: 'n' },
  { prefix: '?', final: 'n' },
  { intermediates: '$', final: 'p' },
  { prefix: '?', intermediates: '$', final: 'p' },
];
const OSC_COLOR_QUERIES = [4, 10, 11, 12];

// swallowQueries makes the terminal ignore every query it would answer. The
// TUI answers none either (its emulator's replies are drained and dropped):
// the page is one of several views of a pane, and its terminal parses replayed
// history as well as live output, so a reply from it would be input to the
// pane that the program never asked this view for. Keyboard and paste input
// are untouched. A color sequence that sets and queries at once is dropped
// whole.
export function swallowQueries(parser: IParser): void {
  for (const id of CSI_QUERIES) parser.registerCsiHandler(id, () => true);
  parser.registerDcsHandler({ intermediates: '$', final: 'q' }, () => true);
  for (const ident of OSC_COLOR_QUERIES) {
    parser.registerOscHandler(ident, (data) => data.split(';').includes('?'));
  }
}
