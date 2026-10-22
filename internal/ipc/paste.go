package ipc

// PasteImageReqPayload asks the daemon to put a pasted clipboard image where
// the pane's agent can read it, and answer with that path.
//
// Only an sbx pane needs it: the TUI saves an image on its own machine and
// types the path, which inside a sandbox names nothing. The image travels in
// the request rather than as a path because the TUI may be on another
// machine than the daemon (--remote, --connect).
type PasteImageReqPayload struct {
	PaneID string `json:"pane_id"`
	// PNG is the image, base64 on the wire. At most MaxPasteImageBytes.
	PNG []byte `json:"png"`
}

// PasteImageRespPayload answers a PasteImageReqPayload. Exactly one of Path
// and Error is set.
type PasteImageRespPayload struct {
	PaneID string `json:"pane_id"`
	// Path is the image's path as the pane's agent sees it.
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// MaxPasteImageBytes bounds an image in a paste_image_req. Base64 grows it by
// a third, so the frame stays under maxFrameSize (10 MiB) with room for the
// envelope. A larger image is refused by the TUI before it is sent.
const MaxPasteImageBytes = 7 * 1024 * 1024
