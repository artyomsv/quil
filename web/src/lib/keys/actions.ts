// Actions with no browser form in 5b. Their chords are still consumed, with a
// notice, so they never type into a pane by surprise (spec §5.5).
export const TUI_ONLY = new Set([
  'app.quit',
  'app.redraw',
  'app.command_palette',
  'pane.notes_toggle',
  'pane.command_history',
  'pane.quick_actions',
  'pane.go_back',
  'pane.toggle_eager',
  'pane.toggle_wrap',
  'pane.focus_toggle',
  'project.new',
  'project.destroy',
  'project.picker',
  'project.next',
  'project.prev',
  'project.toggle',
  'project.attention_queue',
  'project.move_up',
  'project.move_down',
  'project.group_toggle',
  'project.groups_collapse_all',
  'tab.move_left',
  'tab.move_right',
  'tab.layout_even',
  'tab.layout_columns',
  'tab.layout_rows',
  'tab.layout_grid',
  'tab.layout_main',
  'tab.layout_spiral',
]);

// Actions the browser performs itself: the key is left to the browser so its
// own paste event reaches the terminal (the paste flow takes it there).
export const NATIVE = new Set(['pane.paste']);

// Actions a read-only tab may still run: they change only this page.
export const VIEW_ONLY = new Set([
  'notification.toggle',
  'notification.focus',
  'sidebar.toggle',
  'system.shortcuts',
  'pane.left',
  'pane.right',
  'pane.up',
  'pane.down',
  'pane.next',
  'pane.prev',
  'pane.scroll_page_up',
  'pane.scroll_page_down',
]);
