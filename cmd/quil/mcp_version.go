package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// mcpDaemonMinVersion is the first daemon release that answers the request
// types the project, tab, catalog and task tools send (list_projects_req,
// create_tab_req, plugin_catalog_req, delegate_task_req, the *_op_resp
// answers, and the create_pane_req fields toggles / name / worktree_branch /
// sandbox). An older daemon drops an unknown message type SILENTLY, so
// without this check a tool aimed at it hangs for the bridge's full request
// timeout and then reports "timeout" — which reads as a bug in the daemon
// rather than as "this host has not been upgraded". Measured 2026-09-10
// against a remote still on 1.71.0.
//
// Newer tools use their own floor; do not raise the floor of existing tools.
const mcpDaemonMinVersion = "1.72.0"
const createFromTemplateMinVersion = "1.74.0"

// listClientsMinVersion is list_clients' own floor: list_clients_req is new
// with multi-client sync, and the daemon-side handler does not exist before
// it. A separate constant rather than raising mcpDaemonMinVersion — every
// OTHER existing tool must keep working against a daemon that predates this
// feature.
const listClientsMinVersion = "1.80.0"

// daemonVersionProbeTimeout bounds remote version probes. A pre-versioning
// daemon drops the request silently. Local startup uses handshakeTimeout;
// this is a package var so remote-bridge tests can keep it short.
var daemonVersionProbeTimeout = remoteHandshakeTimeout

// probeDaemonVersion asks a freshly dialled client which version its daemon
// runs and which gated request types it handles. It must run BEFORE the
// bridge's readLoop owns the connection, because it reads the reply itself.
// Both empty on any failure — "unknown" — and an unknown daemon is never a
// reason to refuse anything (see requireDaemon).
//
// This is deliberately not versionHandshakeWithin: that one is the TUI's
// GATE and skips itself entirely for a non-release client, while a dev bridge
// still needs the answer to say "that host is older than the tools you are
// calling" instead of timing out.
func probeDaemonVersion(client *ipc.Client, timeout time.Duration) (string, []string) {
	reqID := fmt.Sprintf("mcpv-%d", time.Now().UnixNano())
	req, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		return "", nil
	}
	req.ID = reqID
	if err := client.Send(req); err != nil {
		return "", nil
	}
	if err := client.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		msg, err := client.Receive()
		if err != nil {
			var netErr net.Error
			if !(errors.As(err, &netErr) && netErr.Timeout()) {
				log.Printf("mcp: version probe: %v", err)
			}
			return "", nil
		}
		if msg.Type != ipc.MsgVersionResp || msg.ID != reqID {
			continue
		}
		var payload ipc.VersionRespPayload
		if err := msg.DecodePayload(&payload); err != nil {
			return "", nil
		}
		// Bounded like the version string: both come off the wire from a
		// host the user may not control, and both are rendered in errors.
		reqs := payload.Requests
		if len(reqs) > maxGatedRequests {
			reqs = reqs[:maxGatedRequests]
		}
		for i, r := range reqs {
			reqs[i] = truncateVersion(r)
		}
		return truncateVersion(payload.Version), reqs
	}
}

// maxGatedRequests bounds a list that arrives from another machine.
const maxGatedRequests = 64

// truncateVersion bounds a string that came off the wire from a daemon the
// user may not control; it is rendered in list_hosts and in error text.
func truncateVersion(v string) string {
	const max = 64
	if len(v) > max {
		return v[:max]
	}
	return v
}

// requireDaemon reports whether the daemon behind b is new enough for a tool
// that sends one of the request types listed on mcpDaemonMinVersion.
//
// Unknown and unparseable versions PASS: an unstamped build reports "dev", a
// pre-versioning daemon reports nothing, and refusing either would take a
// developer's own daemon away from them. Only a RELEASE number older than the
// floor is refused, and the error says what to run — the remedy is the same
// one `quil remote setup` performs.
func (b *mcpBridge) requireDaemon(tool string) error {
	return b.requireDaemonAtLeast(tool, mcpDaemonMinVersion)
}

// requireRequest gates a tool on whether the daemon SAYS it handles the
// request type, falling back to the version floor when it does not say.
//
// A version number cannot answer this for a build made from a branch.
// scripts/dev.sh stamps `-X main.version=$(cat VERSION)` into all six
// binaries — dev and debug included — so a client and daemon built here both
// report the tree's VERSION while the floor names the release that has not
// happened yet. Comparing numbers there refuses the daemon the client was
// built beside, and the tool is unusable in exactly the builds used to test
// it. Comparing them the other way — treating an equal number as proof of a
// shared build — is no better: quil-debug.exe attaches to the PRODUCTION
// daemon by design, so a released daemon wearing the same number would be
// sent a request it drops in silence, which is the timeout the floor exists
// to replace.
//
// So the daemon is asked instead. An EMPTY list means "cannot say" — every
// daemon built before the field existed — and the version floor still
// decides there, unchanged. A NON-EMPTY list that omits the type is a
// daemon that answered and does not have it, which is a refusal however new
// its version reads.
func (b *mcpBridge) requireRequest(tool, reqType, min string) error {
	if len(b.daemonRequests) > 0 {
		if slices.Contains(b.daemonRequests, reqType) {
			return nil
		}
		return fmt.Errorf("%s is not available on that daemon (it runs %s and does not handle %s) — upgrade it (quil remote setup <host> pushes this client's build)",
			tool, b.daemonVersion, reqType)
	}
	return b.requireDaemonAtLeast(tool, min)
}

func (b *mcpBridge) requireDaemonAtLeast(tool, min string) error {
	v := b.daemonVersion
	if v == "" {
		return nil
	}
	cmp, err := versionpkg.Compare(v, min)
	if err != nil || cmp >= 0 {
		return nil
	}
	return fmt.Errorf("%s needs quil %s or newer on the daemon, and this one runs %s — upgrade it (quil remote setup <host> pushes this client's build)",
		tool, min, v)
}
