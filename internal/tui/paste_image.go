package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sbx"
)

// Pasting a clipboard image into an sbx pane.
//
// For every other pane the image is saved on this machine and its path typed
// (tryPasteClipboardImage). Inside a sandbox that path names a file the VM
// cannot see, so for an sbx pane the image goes to the daemon instead, which
// copies it into the pane's sandbox and answers with the path inside the VM.
// That path is what gets typed. Nothing is saved on this machine.

// pasteImageTimeout bounds the round trip. The daemon's own copy is bounded at
// 30 s; this leaves room for a slow link carrying several megabytes.
const pasteImageTimeout = 45 * time.Second

// sandboxImagePasteMsg carries an image read on the paste Cmd's goroutine to
// Update, which owns the request bookkeeping. dest is the host the pane was
// on when the user asked to paste, resolved on the Update goroutine BEFORE
// the clipboard read: the image may only ever go to that host.
type sandboxImagePasteMsg struct {
	paneID string
	dest   string
	png    []byte
}

// pasteImageRespMsg is the daemon's answer, or — with local set — a send that
// failed on this side. A local one is not an IPC response, so its Update arm
// must not re-arm listenForMessages.
type pasteImageRespMsg struct {
	Resp  ipc.PasteImageRespPayload
	Gen   string
	local bool
}

// pasteImageTimeoutMsg is a LOCAL timer: its arm must not re-arm
// listenForMessages either.
type pasteImageTimeoutMsg struct{ gen string }

// startSandboxImagePaste sends the image for paneID to dest and records the
// request.
//
// The pane must still exist, on the host it was bound to: a screenshot is
// the one upload here, and the clipboard read between the keypress and this
// call is long enough for the pane to close and another host to become
// active. destOfPane is deliberately NOT used — its fallback for a missing
// pane is the ACTIVE host, which would send the image to a machine the user
// never aimed it at.
func (m *Model) startSandboxImagePaste(paneID, dest string, png []byte) tea.Cmd {
	if len(png) > ipc.MaxPasteImageBytes {
		m.setErrorFlash("Image too large to paste into the sandbox")
		return nil
	}
	pane, proj, _ := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || proj.Dest != dest {
		m.setErrorFlash("Image not pasted: the pane is gone")
		return nil
	}
	if r, routed := m.client.(*Router); routed && r.Conn(dest) == nil {
		m.setErrorFlash("Image not pasted: the pane's host is not connected")
		return nil
	}
	msg, err := ipc.NewMessage(ipc.MsgPasteImageReq, ipc.PasteImageReqPayload{PaneID: paneID, PNG: png})
	if err != nil {
		log.Printf("paste image: encode: %v", err)
		return nil
	}
	gen := m.nextReqGen()
	msg.ID = gen
	stampDest(msg, dest)
	if m.pasteImageReqs == nil {
		m.pasteImageReqs = map[string]string{}
	}
	m.pasteImageReqs[gen] = paneID
	m.setFlash("Copying the image into the sandbox…")
	client := m.client
	return tea.Batch(
		func() tea.Msg {
			if err := client.Send(msg); err != nil {
				return pasteImageRespMsg{Gen: gen, local: true,
					Resp: ipc.PasteImageRespPayload{PaneID: paneID, Error: err.Error()}}
			}
			return nil
		},
		tea.Tick(pasteImageTimeout, func(time.Time) tea.Msg { return pasteImageTimeoutMsg{gen: gen} }),
	)
}

// applyPasteImageResp types the answered path into the pane that asked, or
// says why nothing was pasted. An answer nobody is waiting for — timed out, or
// a stray id — is dropped.
func (m *Model) applyPasteImageResp(msg pasteImageRespMsg) tea.Cmd {
	paneID, ok := m.pasteImageReqs[msg.Gen]
	if !ok {
		return nil
	}
	delete(m.pasteImageReqs, msg.Gen)
	if msg.Resp.Error != "" {
		m.setErrorFlash("Image not pasted: " + sanitizeRemoteText(msg.Resp.Error))
		return nil
	}
	// The daemon may be another machine, and this text is typed into a PTY:
	// only a path of the one shape it produces is accepted.
	if !sbx.ValidPastePath(msg.Resp.Path) {
		log.Printf("paste image: refusing the answered path %q", msg.Resp.Path)
		m.setErrorFlash("Image not pasted: the daemon answered an unexpected path")
		return nil
	}
	// Straight to the pane that asked, never through the typing guard: the
	// image exists only in THAT pane's sandbox, so a remote tab switch while
	// it was copying must not redirect the path to another pane.
	pane, _, _ := m.findPaneAndTab(paneID)
	if pane == nil {
		return nil
	}
	pane.answerBlockedByInput()
	m.enqueueInput(paneID, pastePayload(pane, msg.Resp.Path))
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return pasteRefreshMsg{} })
}

// applyPasteImageTimeout forgets a request the daemon never answered.
func (m *Model) applyPasteImageTimeout(gen string) {
	if _, ok := m.pasteImageReqs[gen]; !ok {
		return
	}
	delete(m.pasteImageReqs, gen)
	m.setErrorFlash("Image not pasted: the sandbox did not answer in time")
}
