// The welcome notice shows at page load while config.toml says show_disclaimer;
// "OK" hides it for this browser tab only (sessionStorage).
export const WELCOME_SEEN_KEY = 'quil.welcome_seen';

export function shouldShowWelcome(show: boolean, seen: string | null): boolean {
  return show && seen !== '1';
}
