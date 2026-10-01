/**
 * Split prose on backticks; odd indexes are code spans.
 * "Run `quil` now" → ["Run ", "quil", " now"].
 * Used instead of set:html so data files never carry markup.
 */
export function splitTicks(text: string): string[] {
  return text.split("`");
}
