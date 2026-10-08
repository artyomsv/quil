// One entry of a Menu (components/Menu.svelte).
export interface MenuItem {
  label: string;
  run: () => void;
  disabled?: boolean;
  // Why a disabled entry cannot run, shown as its tooltip.
  reason?: string;
  // The key that runs the same action in this browser, shown at the right;
  // none when no key works here.
  key?: string;
}
