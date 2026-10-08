export const COPY_FAILED = 'Copy failed — select the text and copy it by hand';

// copyText writes text to the clipboard. The gateway serves loopback
// origins only, which browsers treat as secure contexts, so the API exists
// over http://; a denied permission still throws.
export async function copyText(text: string): Promise<string> {
  try {
    await navigator.clipboard.writeText(text);
    return '';
  } catch {
    return COPY_FAILED;
  }
}
