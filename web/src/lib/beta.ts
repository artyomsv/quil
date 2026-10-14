// The beta notice shows at page load while config.toml says show_disclaimer;
// "OK" hides it for this browser tab only (sessionStorage).
export const BETA_SEEN_KEY = 'quil.beta_seen';

export function shouldShowBeta(show: boolean, seen: string | null): boolean {
  return show && seen !== '1';
}
