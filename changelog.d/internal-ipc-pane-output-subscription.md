- Clients can now decline the live pane-output broadcast (`subscribe` message), and the MCP
  bridge does. The daemon broadcast every pane's PTY bytes to every connection, and the
  bridge decoded each frame only to discard it — so a workspace with many AI sessions
  attached multiplied one verbose pane's output by the connection count, in both socket
  writes and frame decoding.

  Nothing is opted out by default: a client that never sends the message, including any
  older build, receives exactly what it did before. Only the live output stream can be
  declined — workspace state, request responses and lifecycle frames are unaffected.
