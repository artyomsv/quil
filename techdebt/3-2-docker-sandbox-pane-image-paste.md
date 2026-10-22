# Image paste does not reach a docker sandbox pane

| Field | Value |
|-------|-------|
| Criticality | Medium |
| Complexity | Small |
| Location | `internal/tui/model.go` `pasteClipboard`, `internal/daemon/paste_image.go` |
| Found during | Feature dev: image paste into sbx panes |
| Date | 2026-10-22 |

## Issue

A pasted clipboard image is saved under `$QUIL_HOME/paste` on the TUI's
machine and its host path is typed into the pane. The sbx backend now routes
the image through `paste_image_req` and copies it into the VM. A DOCKER
sandbox pane (`SandboxImage` set) still gets the host path, and its container
does not mount `$QUIL_HOME/paste` — the mount set is the boundary and must not
grow for this.

## Risks

The agent in a docker sandbox pane cannot read a pasted screenshot; it reports
the file missing.

## Suggested Solutions

1. Extend `paste_image_req` to docker panes: `docker cp` into the pane's
   container (labels `quil.pane` + `quil.home`), then `docker exec -u root
   chmod`, the same two steps as `sbx.CopyIn`. The TUI gate becomes
   `SandboxKind != ""`.
2. Write the image into the pane's own per-pane tree that is already mounted
   (`$QUIL_HOME/sandbox/panes/<id>`), and type the container-side path. No new
   docker call, but the tree is agent-writable, so the write must go through
   `os.Root` like every other daemon access there.
