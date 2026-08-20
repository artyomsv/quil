---
headline: Session titles no longer show tool output or slash commands
---
- **The Claude session picker no longer titles a session from something you did not
  type.** Sessions were showing tool output, slash-command markup, injected hook
  notices, and in a few cases a single letter or a directory path, because the check
  meant to keep those out tested one entry at a time while describing itself as a
  property of the whole transcript.

  A session's title is now taken only from an entry that is actually a prompt.
  Anything Claude wrote into the conversation itself — a tool result, a
  `/command` expansion, a system reminder — is passed over, and the next real
  prompt is used instead. Where a transcript holds no prompt at all, the picker
  shows the session id rather than inventing a label from whatever came first.

  The same entries were being counted as prompts in the detail panel, so its
  prompt count and first/last prompt were wrong in the same sessions. Those are
  corrected by the same check.
