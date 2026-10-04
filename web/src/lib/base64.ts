const encoder = new TextEncoder();

export function bytesToBase64(b: Uint8Array): string {
  let s = '';
  const chunk = 0x8000;
  for (let i = 0; i < b.length; i += chunk) s += String.fromCharCode(...b.subarray(i, i + chunk));
  return btoa(s);
}

// The daemon's pane_input carries []byte, which Go's JSON encodes as base64.
export function utf8ToBase64(s: string): string {
  return bytesToBase64(encoder.encode(s));
}
