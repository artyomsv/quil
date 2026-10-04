package webgw

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

type frameVector struct {
	Name       string `json:"name"`
	PaneID     string `json:"pane_id"`
	Ghost      bool   `json:"ghost"`
	Generation string `json:"generation"`
	DataHex    string `json:"data_hex"`
	FrameHex   string `json:"frame_hex"`
}

// The vectors are shared with the browser's decoder test, so both sides are
// held to one byte layout rather than each round-tripping with itself.
func TestEncodePaneOutput_Vectors(t *testing.T) {
	b, err := os.ReadFile("testdata/frame_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []frameVector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			gen, err := strconv.ParseUint(v.Generation, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			data, err := hex.DecodeString(v.DataHex)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodePaneOutput(ipc.PaneOutputPayload{PaneID: v.PaneID, Data: data, Ghost: v.Ghost, Generation: gen})
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != v.FrameHex {
				t.Fatalf("got  %x\nwant %s", got, v.FrameHex)
			}
		})
	}
}

func TestEncodePaneOutput_RefusesBadPaneIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 256)} {
		if _, err := EncodePaneOutput(ipc.PaneOutputPayload{PaneID: id}); !errors.Is(err, ErrBadPaneID) {
			t.Fatalf("pane id of %d bytes: err = %v", len(id), err)
		}
	}
	if _, err := EncodePaneOutput(ipc.PaneOutputPayload{PaneID: strings.Repeat("x", 255)}); err != nil {
		t.Fatalf("255-byte id refused: %v", err)
	}
}
