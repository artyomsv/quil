// One entry of a Menu (components/Menu.svelte).
export interface MenuItem {
  label: string;
  run: () => void;
  disabled?: boolean;
}
