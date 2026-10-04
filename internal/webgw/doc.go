// Package webgw is the quil web gateway: an HTTP server on loopback that
// serves the embedded browser client and bridges each browser tab's WebSocket
// to its own daemon connection. JSON messages pass through unchanged,
// pane_output becomes a binary frame, and an allow-list bounds what a page
// may send. The daemon is unaware of it: each tab is an ordinary client.
package webgw
