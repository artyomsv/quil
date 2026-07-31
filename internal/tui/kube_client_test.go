package tui

import (
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// kubeClientModel builds a minimal Model with a fake IPC client, sitting in
// the setup dialog. applyKubeContexts drops any response that arrives while a
// different dialog (or none) is open — see the dialog-closed guard mirrored
// from applyGitReposPickList — so every test that expects an apply to stick
// needs this precondition already true.
func kubeClientModel(t *testing.T) *Model {
	t.Helper()
	return &Model{
		client: &fakeSender{},
		dialog: dialogCreatePaneSetup,
	}
}

// The generation must reach the wire, or the daemon cannot echo the staleness
// key back and two overlapping requests become indistinguishable.
func TestRequestKubeContexts_CarriesGenerationOnTheWire(t *testing.T) {
	m := kubeClientModel(t)
	cmd := m.requestKubeContexts()
	if cmd == nil {
		t.Fatal("requestKubeContexts returned no command")
	}
	// runCmd, NOT cmd(): the request returns a tea.Batch, and calling a batch
	// yields a BatchMsg without executing its children — nothing would be sent
	// even against a correct implementation. See the plan's "Test harness" note.
	runCmd(cmd)
	sent := m.client.(*fakeSender).sent
	if len(sent) == 0 {
		t.Fatal("no message sent")
	}
	msg := sent[len(sent)-1]
	if msg.Type != ipc.MsgKubeCtxReq {
		t.Errorf("Type = %q, want %q", msg.Type, ipc.MsgKubeCtxReq)
	}
	if msg.ID == "" || msg.ID != m.kubeScan.gen {
		t.Errorf("sent ID = %q, want the recorded generation %q — the staleness key never reaches the daemon", msg.ID, m.kubeScan.gen)
	}
	if m.kubeScan.phase != kubeScanning {
		t.Errorf("phase = %v, want kubeScanning", m.kubeScan.phase)
	}
}

func TestApplyKubeContexts_StaleGenerationIsDropped(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	m.applyKubeContexts(ipc.KubeCtxRespPayload{
		Contexts: []ipc.KubeContextInfo{{Name: "from-a-superseded-request"}},
	}, "not-the-current-gen")
	if len(m.kubeContexts) != 0 {
		t.Errorf("kubeContexts = %v, want empty — a superseded response was applied", m.kubeContexts)
	}
}

func TestApplyKubeContexts_PopulatesAndMarksReady(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	m.applyKubeContexts(ipc.KubeCtxRespPayload{
		Contexts: []ipc.KubeContextInfo{
			{Name: "ctx-a", Namespace: "ns-a"},
			{Name: "ctx-b", Current: true},
		},
	}, m.kubeScan.gen)

	if len(m.kubeContexts) != 2 {
		t.Fatalf("kubeContexts = %d, want 2", len(m.kubeContexts))
	}
	if m.kubeContexts[0].Namespace != "ns-a" {
		t.Errorf("Namespace = %q, want ns-a", m.kubeContexts[0].Namespace)
	}
	if !m.kubeContexts[1].Current {
		t.Error("Current = false — the marker was lost converting off the wire")
	}
	if m.kubeScan.phase != kubeScanReady {
		t.Errorf("phase = %v, want kubeScanReady", m.kubeScan.phase)
	}
	if m.kubeCursor != 0 {
		t.Errorf("kubeCursor = %d, want 0 (Default context row)", m.kubeCursor)
	}
}

// A cap enforced only by the sender is not a cap: the daemon is the untrusted
// side now.
func TestApplyKubeContexts_CapsAnOversizedResponse(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	big := make([]ipc.KubeContextInfo, maxKubeContexts+5)
	for i := range big {
		big[i] = ipc.KubeContextInfo{Name: fmt.Sprintf("ctx-%d", i)}
	}
	m.applyKubeContexts(ipc.KubeCtxRespPayload{Contexts: big}, m.kubeScan.gen)
	if len(m.kubeContexts) != maxKubeContexts {
		t.Errorf("kubeContexts = %d, want capped to %d", len(m.kubeContexts), maxKubeContexts)
	}
}

// A failed scan must not render as "no kube contexts found" — that is the
// wrong-answer-stated-confidently failure this phase exists to remove.
func TestApplyKubeContexts_ErrorMarksFailedNotEmpty(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	m.applyKubeContexts(ipc.KubeCtxRespPayload{Error: "kube context scan timed out"}, m.kubeScan.gen)
	if m.kubeScan.phase != kubeScanFailed {
		t.Errorf("phase = %v, want kubeScanFailed", m.kubeScan.phase)
	}
}

func TestApplyKubeScanTimeout_StaleGenerationDoesNotClearALiveScan(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	live := m.kubeScan.gen
	m.applyKubeScanTimeout("an-older-gen")
	if m.kubeScan.gen != live || m.kubeScan.phase != kubeScanning {
		t.Error("a superseded timeout tick cleared the live scan")
	}
}

// The daemon's Truncated flag must survive the wire crossing, or a
// 55-context kubeconfig renders 50 rows and says nothing about the other 5.
func TestApplyKubeContexts_SetsTruncatedFromResponse(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	m.applyKubeContexts(ipc.KubeCtxRespPayload{
		Contexts:  []ipc.KubeContextInfo{{Name: "ctx-a"}},
		Truncated: true,
	}, m.kubeScan.gen)
	if !m.kubeTruncated {
		t.Error("kubeTruncated = false, want true — the daemon's Truncated flag was dropped")
	}
}

// The OR with the client-side cap check is load-bearing: a daemon that
// over-sends without setting Truncated must still be caught, the same way
// TestApplyKubeContexts_CapsAnOversizedResponse pins the length cap itself.
func TestApplyKubeContexts_TruncatedAlsoInferredFromOversizedResponse(t *testing.T) {
	m := kubeClientModel(t)
	m.requestKubeContexts()
	big := make([]ipc.KubeContextInfo, maxKubeContexts+5)
	for i := range big {
		big[i] = ipc.KubeContextInfo{Name: fmt.Sprintf("ctx-%d", i)}
	}
	m.applyKubeContexts(ipc.KubeCtxRespPayload{Contexts: big, Truncated: false}, m.kubeScan.gen)
	if !m.kubeTruncated {
		t.Error("kubeTruncated = false, want true — an oversized response with Truncated=false must still be caught")
	}
}

// A stale truncation marker left over from a previous scan must not survive
// into a new one — it would render as "capped" for content the new scan
// hasn't even reported yet.
func TestRequestKubeContexts_ClearsStaleTruncated(t *testing.T) {
	m := kubeClientModel(t)
	m.kubeTruncated = true
	m.requestKubeContexts()
	if m.kubeTruncated {
		t.Error("kubeTruncated survived a new request")
	}
}
