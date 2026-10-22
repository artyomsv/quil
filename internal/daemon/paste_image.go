package daemon

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sbx"
)

// A pasted image for an sbx pane.
//
// The TUI pastes a clipboard image by saving it and typing its path, which
// inside a sandbox names a file the VM cannot see. For an sbx pane the TUI
// sends the image here instead; the daemon — the side that runs sbx — copies
// it into the sandbox the pane's agent runs in and answers with the path
// inside the VM. The copy goes to that one sandbox only.

// pngMagic is the PNG signature. The TUI encodes every clipboard image as PNG,
// so anything else is not one of its pastes.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// handlePasteImageReq answers from a worker: the copy is two sbx calls, which
// must not run on the conn's dispatch goroutine. The worker keeps the
// request's ID and the image, never the *ipc.Message.
func (d *Daemon) handlePasteImageReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.PasteImageReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgPasteImageResp, ipc.PasteImageRespPayload{Error: "bad request: " + err.Error()})
		return
	}
	// The cheap refusals answer here, before the slot: a bad request must not
	// hold it.
	if err := checkPasteImage(req.PNG); err != nil {
		respondTo(conn, msg.ID, ipc.MsgPasteImageResp, ipc.PasteImageRespPayload{PaneID: req.PaneID, Error: err.Error()})
		return
	}
	if !d.pasteCopying.CompareAndSwap(false, true) {
		respondTo(conn, msg.ID, ipc.MsgPasteImageResp, ipc.PasteImageRespPayload{
			PaneID: req.PaneID, Error: "another image is still being copied — paste again in a moment",
		})
		return
	}
	id := msg.ID
	go func() {
		defer d.pasteCopying.Store(false)
		respondTo(conn, id, ipc.MsgPasteImageResp, d.pasteImageResponse(context.Background(), req))
	}()
}

// pasteImageResponse validates the request, copies the image and builds the
// answer. Split from the handler so a test can drive it without an ipc.Conn.
func (d *Daemon) pasteImageResponse(ctx context.Context, req ipc.PasteImageReqPayload) ipc.PasteImageRespPayload {
	resp := ipc.PasteImageRespPayload{PaneID: req.PaneID}
	path, err := d.copyPasteImage(ctx, req)
	if err != nil {
		resp.Error = err.Error()
		log.Printf("paste image: pane %s: %v", req.PaneID, err)
		return resp
	}
	log.Printf("paste image: pane %s: %d bytes → %s", req.PaneID, len(req.PNG), path)
	resp.Path = path
	return resp
}

func (d *Daemon) copyPasteImage(ctx context.Context, req ipc.PasteImageReqPayload) (string, error) {
	if err := checkPasteImage(req.PNG); err != nil {
		return "", err
	}
	name, err := d.sbxSandboxOfPane(req.PaneID)
	if err != nil {
		return "", err
	}
	bin, err := sbxBinaryFn()
	if err != nil {
		return "", err
	}
	id, err := pasteImageID(time.Now())
	if err != nil {
		return "", err
	}
	// A private temp copy on this machine: sbx cp reads a host file. It is
	// removed once copied, so the daemon keeps no screenshot of its own.
	f, err := os.CreateTemp("", "quil-paste-*.png")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	src := f.Name()
	defer os.Remove(src)
	_, werr := f.Write(req.PNG)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("temp file: %w", werr)
	}
	return sbx.Client{Bin: bin, Runner: sbxRunner}.CopyIn(ctx, name, src, id)
}

// checkPasteImage refuses an image of the wrong size or format.
func checkPasteImage(png []byte) error {
	if len(png) == 0 || len(png) > ipc.MaxPasteImageBytes {
		return fmt.Errorf("image size %d bytes is outside 1..%d", len(png), ipc.MaxPasteImageBytes)
	}
	if !bytes.HasPrefix(png, pngMagic) {
		return errors.New("not a PNG image")
	}
	return nil
}

// sbxSandboxOfPane names the sandbox the pane's agent runs in now. Read off
// the running session rather than re-derived from the pane: a profile pane's
// sandbox name comes from its approved profile, which is not read again here.
func (d *Daemon) sbxSandboxOfPane(paneID string) (string, error) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return "", errors.New("no such pane")
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.SandboxBackend != config.SandboxBackendSbx {
		return "", errors.New("the pane does not run in an sbx sandbox")
	}
	s, ok := pane.PTY.(*sbxSession)
	if !ok || pane.ExitCode != nil || s.name == "" {
		return "", errors.New("the pane's agent is not running")
	}
	return s.name, nil
}

// pasteImageID names the paste folder in the VM: a timestamp for the reader,
// and 64 random bits, which are what make the name unguessable to the agent
// (see internal/sbx/paste.go).
func pasteImageID(now time.Time) (string, error) {
	suffix := make([]byte, 8)
	if _, err := crand.Read(suffix); err != nil {
		return "", fmt.Errorf("random name: %w", err)
	}
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(suffix)), nil
}
