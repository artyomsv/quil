---
headline: Clients say hello, and a bad update never blanks the workspace
---
- Every client now says `hello` to the daemon. A request the daemon does not know, or cannot read, gets an error reply instead of a silent timeout — the MCP bridge shows that error.
- Workspace updates are numbered. A client drops an out-of-date update, and an update it cannot read is never applied as an empty workspace; it asks the daemon for a full copy instead.
