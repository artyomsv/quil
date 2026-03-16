package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message type constants
const (
	// Lifecycle
	MsgAttach    = "attach"
	MsgDetach    = "detach"
	MsgShutdown  = "shutdown"
	MsgHeartbeat = "heartbeat"

	// Session control (Client -> Daemon)
	MsgCreatePane   = "create_pane"
	MsgDestroyPane  = "destroy_pane"
	MsgResizePane   = "resize_pane"
	MsgUpdatePane   = "update_pane"
	MsgUpdateLayout = "update_layout"

	// Tab control (Client -> Daemon)
	MsgCreateTab  = "create_tab"
	MsgDestroyTab = "destroy_tab"
	MsgSwitchTab  = "switch_tab"
	MsgUpdateTab  = "update_tab"

	// I/O (bidirectional)
	MsgPaneInput  = "pane_input"
	MsgPaneOutput = "pane_output"

	// State sync (Daemon -> Client)
	MsgWorkspaceState = "workspace_state"
	MsgStateUpdate    = "state_update"

	// Plugin (Daemon -> Client)
	MsgPluginError = "plugin_error"
)

// Message is the wire format for IPC communication.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Payload types

type AttachPayload struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type CreatePanePayload struct {
	TabID         string   `json:"tab_id"`
	CWD           string   `json:"cwd"`
	Type          string   `json:"type,omitempty"`
	InstanceName  string   `json:"instance_name,omitempty"`
	InstanceArgs  []string `json:"instance_args,omitempty"`
	ReplacePaneID string   `json:"replace_pane_id,omitempty"`
}

type DestroyPanePayload struct {
	PaneID string `json:"pane_id"`
}

type ResizePanePayload struct {
	PaneID string `json:"pane_id"`
	Rows   uint16 `json:"rows"`
	Cols   uint16 `json:"cols"`
}

type PaneInputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
}

type PaneOutputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
	Ghost  bool   `json:"ghost,omitempty"`
}

type CreateTabPayload struct {
	Name string `json:"name"`
}

type DestroyTabPayload struct {
	TabID string `json:"tab_id"`
}

type SwitchTabPayload struct {
	TabID string `json:"tab_id"`
}

type UpdateTabPayload struct {
	TabID string `json:"tab_id"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

type UpdatePanePayload struct {
	PaneID string `json:"pane_id"`
	Name   string `json:"name,omitempty"`
	CWD    string `json:"cwd,omitempty"`
}

type UpdateLayoutPayload struct {
	TabID  string          `json:"tab_id"`
	Layout json.RawMessage `json:"layout"`
}

type PluginErrorPayload struct {
	PaneID  string `json:"pane_id"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// NewMessage creates a Message with a typed payload.
func NewMessage(typ string, payload any) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{Type: typ, Payload: data}, nil
}

// WriteMessage writes a length-prefixed JSON message to w.
// Format: [4 bytes uint32 big-endian length][JSON payload]
func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed JSON message from r.
func ReadMessage(r io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}

	if length > 10*1024*1024 { // 10MB max
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// DecodePayload unmarshals the message payload into the given target.
func (m *Message) DecodePayload(target any) error {
	return json.Unmarshal(m.Payload, target)
}
