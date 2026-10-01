import { execFileSync } from "node:child_process";

/**
 * Newest commit date (strict ISO 8601) over `files` — paths or directories
 * relative to site/ — or undefined when git has no answer: no repository,
 * a shallow or foreign checkout, an untracked file.
 *
 * Undefined is deliberate. The old sitemap fell back to the build time,
 * which tells a crawler every page changed on every deploy.
 */
export function lastModified(files: readonly string[]): string | undefined {
  try {
    const out = execFileSync("git", ["log", "-1", "--format=%cI", "--", ...files], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    return out || undefined;
  } catch {
    return undefined;
  }
}
