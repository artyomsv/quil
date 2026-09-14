package main

import (
	"errors"
	"fmt"
	"log"
	"net"
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

// daemonVersionProbeTimeout bounds remote version probes. A pre-versioning
// daemon drops the request silently. Local startup uses handshakeTimeout;
// this is a package var so remote-bridge tests can keep it short.
var daemonVersionProbeTimeout = remoteHandshakeTimeout

// probeDaemonVersion asks a freshly dialled client which version its daemon
// runs. It must run BEFORE the bridge's readLoop owns the connection, because
// it reads the reply itself. Empty on any failure — "unknown" — and an
// unknown version is never a reason to refuse anything (see requireDaemon).
//
// This is deliberately not versionHandshakeWithin: that one is the TUI's
// GATE and skips itself entirely for a non-release client, while a dev bridge
// still needs the number to say "that host is older than the tools you are
// calling" instead of timing out.
func probeDaemonVersion(client *ipc.Client, timeout time.Duration) string {
	reqID := fmt.Sprintf("mcpv-%d", time.Now().UnixNano())
	req, err := ipc.NewMessage(ipc.MsgVersionReq, struct{}{})
	if err != nil {
		return ""
	}
	req.ID = reqID
	if err := client.Send(req); err != nil {
		return ""
	}
	if err := client.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return ""
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		msg, err := client.Receive()
		if err != nil {
			var netErr net.Error
			if !(errors.As(err, &netErr) && netErr.Timeout()) {
				log.Printf("mcp: version probe: %v", err)
			}
			return ""
		}
		if msg.Type != ipc.MsgVersionResp || msg.ID != reqID {
			continue
		}
		var payload ipc.VersionRespPayload
		if err := msg.DecodePayload(&payload); err != nil {
			return ""
		}
		return truncateVersion(payload.Version)
	}
}

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
//
// A daemon reporting exactly THIS CLIENT'S OWN version also passes; see
// requireDaemonAtLeast.
func (b *mcpBridge) requireDaemon(tool string) error {
	return b.requireDaemonAtLeast(tool, mcpDaemonMinVersion)
}

func (b *mcpBridge) requireDaemonAtLeast(tool, min string) error {
	v := b.daemonVersion
	if v == "" {
		return nil
	}
	// A daemon reporting exactly this client's own version IS this client's
	// own build, so their wire types agree by construction and no floor can
	// say anything useful about the pair.
	//
	// Without this, a floor naming an UNRELEASED version makes its tool
	// unusable in every build produced from the branch that adds it.
	// scripts/dev.sh stamps `-X main.version=$(cat VERSION)` into all six
	// binaries — dev and debug included — so a locally built pair both report
	// the tree's VERSION, 1.73.0 while 1.74.0 is still unreleased, and the
	// client refuses its own daemon. The comment above this function used to
	// claim a dev daemon reports "dev"; it does not, and believing that is
	// what shipped the refusal.
	//
	// The narrow cost: quil-debug.exe deliberately attaches to the PRODUCTION
	// daemon, so a debug build made here against a released daemon wearing the
	// same number now passes this gate and pays a request timeout instead of a
	// named refusal. That is the pre-floor behaviour, for one variant, during
	// development only.
	if v == version {
		return nil
	}
	cmp, err := versionpkg.Compare(v, min)
	if err != nil || cmp >= 0 {
		return nil
	}
	return fmt.Errorf("%s needs quil %s or newer on the daemon, and this one runs %s — upgrade it (quil remote setup <host> pushes this client's build)",
		tool, min, v)
}
