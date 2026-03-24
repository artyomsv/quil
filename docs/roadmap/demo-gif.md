# The "Holy Shit" Demo GIF/Video

| Field | Value |
|-------|-------|
| Priority | 2 |
| Effort | Small |
| Impact | Critical |
| Status | Proposed |
| Depends on | Pre-built binaries (for install step) |

## Problem

Adoption for developer tools is driven by a single viral moment. A 30-second visual that makes someone say "I need this" is worth more than 10 pages of documentation. Aethel currently has no visual demo — the README describes features but doesn't show them.

## Proposed Solution

Create a 30-second GIF/video that demonstrates Aethel's core value proposition in one continuous take:

1. Show 5 panes: Claude Code mid-conversation, SSH tunnel, build watcher, webhook listener, terminal
2. `sudo reboot`
3. Login, type `aethel`
4. **Everything snaps back** — Claude conversation resumed, build re-watching, SSH reconnected
5. Total time: 3 seconds

This GIF goes on the README, gets posted to Hacker News, r/programming, Twitter/X. It's the entire pitch in one visual.

## User Experience

The demo isn't a feature — it's marketing. But it should demonstrate real, reproducible behavior that any user can replicate after installing Aethel.

## Technical Approach

### Recording Setup

- **Tool**: [VHS](https://github.com/charmbracelet/vhs) (terminal GIF recorder by Charm, integrates well with Bubble Tea) or asciinema + gif conversion
- **Resolution**: 120x40 terminal, large font for readability on social media
- **Duration**: 25-30 seconds max

### Script

```
# Scene 1: The productive workspace (5s)
[Show Aethel with 5 panes in 2 tabs]
[Claude Code pane shows active conversation]
[Build watcher shows "watching..."]
[Webhook listener shows "Ready"]

# Scene 2: The disaster (3s)
[Type in terminal pane: sudo reboot]
[Screen goes black]

# Scene 3: The recovery (5s)
[Login screen → type aethel]
[Ghost buffers render instantly]
[Claude Code resumes conversation]
[Build watcher re-starts]
[SSH reconnects]

# Scene 4: The punchline (2s)
[Hold on fully restored workspace]
[Text overlay: "aethel — reboot-proof terminal sessions"]
```

### Optimization

- Compress GIF to < 5MB for GitHub README inline display
- Also produce MP4 for Twitter/social media
- Host high-quality version externally (YouTube, project website)

## Success Criteria

- [ ] GIF is < 5MB and renders inline on GitHub README
- [ ] Demo shows real reboot → full restore in under 5 seconds
- [ ] README updated with embedded GIF above the fold
- [ ] Social media posts drafted for HN, Reddit, Twitter/X

## Open Questions

- VHS vs asciinema vs screen recording?
- Should the GIF show the install step too? (adds length but shows ease)
- Real reboot vs simulated (daemon restart)?
