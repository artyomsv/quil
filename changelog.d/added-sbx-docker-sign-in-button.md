---
headline: An sbx pane can sign you back in to Docker
---
- **An sbx sandbox pane now helps you sign back in to Docker.** When the Docker
  sign-in of `sbx` expired, the pane only said `sbx is not signed in to Docker. Run:
  sbx login`, and Alt+R failed the same way until you ran `sbx login` somewhere else.

  The pane now opens a dialog with a **Sign in** button. It runs `sbx login` in the
  pane, so you see the code and the browser step there. When the sign-in succeeds,
  the pane starts again, and so does every other sbx pane that stopped for the same
  reason. Quil does not sign in for you and keeps no Docker credential.
