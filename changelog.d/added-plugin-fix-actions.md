---
headline: Tool error dialogs now offer the fix, and an Update button
---
- **A tool pane that fails for a known reason now offers the fix as a button.** A plugin's
  error dialog can carry fix actions (`[[error_handlers.fixes]]`). Pressing one runs the fix
  in that same pane, visibly, and starts the pane's normal command again when it succeeds.
  The dialog opens on its own, so Enter only ever closes it: a fix needs the arrow keys or
  its letter.

- **An outdated tool offers to update itself.** A plugin can declare how its binary is
  updated (`[update]`). When the tool prints its own "a newer version is available" line,
  Quil opens a dialog with **Update** and **Not now**. Update runs in the pane: through the
  package manager that owns the binary (brew, scoop, winget) when one does, otherwise by
  downloading that exact release from GitHub, checking its published SHA-256 and swapping
  the binary in place with a backup to roll back to. Nothing in a system directory is ever
  replaced, and nothing installed by a package manager Quil does not know. Update needs a
  full-access token; Not now holds until a newer version than the one declined is reported.

- **Stripe is the first plugin wired up.** Its login dialog now also fires on an expired
  key (`api_key_expired`), carries a **Log in** button that runs `stripe login` in the pane
  and resumes `stripe listen`, and the pane offers to update the Stripe CLI when it reports
  one. `stripe listen` now passes `--all-snapshot`, which Stripe CLI releases since
  September 2026 require (it forwards every event, as before); a pane started without it
  explains the missing filter, and an older CLI that does not know the flag points at
  Update. `stripe.toml`'s schema version moved to 2, so **every install sees the plugin
  migration dialog once** on first start after this upgrade: press `A` to take the new
  default, or merge your own edits and press Enter.
