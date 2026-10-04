// What the end-to-end tests read from the page. Registered as
// window.__quilTest only in a build made with VITE_QUIL_E2E=1; release
// builds never expose it.
export interface QuilTestHook {
  // The pane's terminal buffer as text, trailing blank lines removed; ''
  // for a pane the page does not hold.
  bufferText(paneId: string): string;
  // One row of the pane's screen (0 is the top), trailing blanks removed;
  // '' for a pane the page does not hold.
  screenLine(paneId: string, row: number): string;
  // The client id the gateway leased to this tab; '' before the welcome.
  clientId(): string;
}

export function shouldRegisterE2EHook(env: Record<string, unknown>): boolean {
  return env.VITE_QUIL_E2E === '1';
}
