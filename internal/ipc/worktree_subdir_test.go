package ipc_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestWorktreeSpec_Subdir_OmittedWhenEmptyAndRoundTrips(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	for _, subdir := range []string{"", "nested/path"} {
		want := ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/test", Subdir: subdir}
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if subdir == "" && string(data) != `{"repo_root":"/repo","branch":"feat/test"}` {
			t.Fatalf("empty Subdir changed wire shape: %s", data)
		}
		var got ipc.WorktreeSpec
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	data, err := json.Marshal(ipc.CreatePaneRespPayload{InvalidSubdir: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "subdir") {
		t.Fatalf("worker-only field leaked to IPC: %s", data)
	}
}
