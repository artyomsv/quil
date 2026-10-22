---
headline: An sbx sandbox pane now resumes its Claude conversation after a restart
---
- An sbx sandbox pane keeps its Claude session: Quil gives the agent a session id at the first start, keeps it on the host with the pane, and on every later start — Alt+R, a daemon restart, a restored workspace — asks the sandbox whether that session has a transcript and resumes it when it does. Before, every start was a fresh conversation and the one in progress was lost, although its transcript was still in the sandbox.
