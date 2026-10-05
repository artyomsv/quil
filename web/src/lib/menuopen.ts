// At most one menu is open on the page: opening one closes the other. The
// record is page-wide, so a menu that is destroyed must drop itself from it,
// or the next menu to open would call a dead menu's close — and an auto menu's
// close runs its onclose, which would act on a picker that no longer exists.
let current: (() => void) | null = null;

// menuOpened records shut as the open menu's close, closing another first.
export function menuOpened(shut: () => void): void {
  if (current && current !== shut) current();
  current = shut;
}

// menuGone forgets shut when it is the recorded one (the menu was destroyed).
export function menuGone(shut: () => void): void {
  if (current === shut) current = null;
}
