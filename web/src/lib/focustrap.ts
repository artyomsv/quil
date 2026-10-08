// A modal keeps Tab and Shift+Tab inside itself: without this, Tab walks out
// of a dialog into the terminal's hidden textarea and the next keys typed
// reach the pane behind the dialog.

const FOCUSABLE =
  'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])';

// trapIndex is where Tab moves among count focusable elements from index at
// (-1: focus is on none of them): wrapping at either end, entering at the
// first (Shift+Tab: the last), and null where the browser's own move stays
// inside.
export function trapIndex(count: number, at: number, back: boolean): number | null {
  if (count === 0) return null;
  if (at < 0) return back ? count - 1 : 0;
  if (!back && at === count - 1) return 0;
  if (back && at === 0) return count - 1;
  return null;
}

// trapFocus is a Svelte action for a modal's root. It listens on the
// document in the capture phase, so a Tab pressed while the focus is
// already outside (in a terminal) is pulled back in too.
export function trapFocus(node: HTMLElement): { destroy(): void } {
  traps.push(node);
  const onKey = (e: KeyboardEvent): void => {
    if (e.key !== 'Tab' || e.ctrlKey || e.altKey || e.metaKey) return;
    // A dialog covered by another one (a confirm over its list, session
    // details over the create dialog) leaves Tab to the one on top.
    if (traps.top() !== node) return;
    const list = Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE));
    const active = document.activeElement as HTMLElement | null;
    const at = active ? list.indexOf(active) : -1;
    if (list.length === 0) {
      e.preventDefault();
      return;
    }
    const next = trapIndex(list.length, at, e.shiftKey);
    if (next === null) return;
    e.preventDefault();
    list[next]?.focus();
  };
  document.addEventListener('keydown', onKey, true);
  return {
    destroy: () => {
      document.removeEventListener('keydown', onKey, true);
      traps.remove(node);
    },
  };
}

// TrapStack orders the open modals: the last one mounted is on top. A
// removed one leaves the order of the others as it was.
export class TrapStack<T> {
  private readonly items: T[] = [];
  push(x: T): void {
    this.items.push(x);
  }
  remove(x: T): void {
    const i = this.items.lastIndexOf(x);
    if (i >= 0) this.items.splice(i, 1);
  }
  top(): T | undefined {
    return this.items[this.items.length - 1];
  }
}

const traps = new TrapStack<HTMLElement>();
