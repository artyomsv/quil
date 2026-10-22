---
headline: Pasted screenshots reach the agent in an sbx sandbox
---
- **Pasting a screenshot into an sbx pane works.** Quil saved a pasted image on
  your machine and typed its path, which names nothing inside the sandbox, so
  the agent answered "file not found". For an sbx pane Quil now copies the
  image into that pane's sandbox (`/tmp/quil-paste-…/image.png`, readable by the agent)
  and types the path inside the sandbox. Only that one image goes into that
  one sandbox. This also works when the daemon runs on another machine.
