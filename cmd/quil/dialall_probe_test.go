package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
)

// stubProbeDestBatch replaces the batch probe and records which destinations
// it was asked about.
func stubProbeDestBatch(t *testing.T, p remoteinstall.Probe, err error) *[]string {
	t.Helper()
	prev := probeDestBatchFn
	t.Cleanup(func() { probeDestBatchFn = prev })
	var asked []string
	probeDestBatchFn = func(dest string) (remoteinstall.Probe, error) {
		asked = append(asked, dest)
		return p, err
	}
	return &asked
}

// Exit 1 before any byte is what cmd and PowerShell answer for a missing
// command AND what quil answers when it refuses to start, so a background
// host is probed (batch, no prompt) to tell the two apart.
func TestClassifyDialFailure_ExitOne_AsksTheHost(t *testing.T) {
	cause := errors.New("no version response from win01")
	exitOne := fakeLink{exitCode: 1}

	t.Run("no quil there is a missing install", func(t *testing.T) {
		asked := stubProbeDestBatch(t, remoteinstall.Probe{OS: "windows"}, nil)
		err := classifyDialFailure("win01", exitOne, cause)
		if !errors.Is(err, tui.ErrRemoteQuilMissing) {
			t.Errorf("err = %v, want ErrRemoteQuilMissing", err)
		}
		if len(*asked) != 1 || (*asked)[0] != "win01" {
			t.Errorf("probed %v, want exactly [win01]", *asked)
		}
	})

	t.Run("quil there exited on its own", func(t *testing.T) {
		stubProbeDestBatch(t, remoteinstall.Probe{OS: "windows", ExistingPath: `C:\q\quil.exe`}, nil)
		err := classifyDialFailure("win01", exitOne, cause)
		if errors.Is(err, tui.ErrRemoteQuilMissing) {
			t.Errorf("err = %v, offered an install for a quil that is present", err)
		}
		if !strings.Contains(err.Error(), "exited before its daemon answered") || !errors.Is(err, cause) {
			t.Errorf("err = %v, want the exit explained and the cause kept", err)
		}
	})

	t.Run("a failed probe changes nothing", func(t *testing.T) {
		stubProbeDestBatch(t, remoteinstall.Probe{}, errors.New("Permission denied (publickey)"))
		if err := classifyDialFailure("win01", exitOne, cause); err != cause {
			t.Errorf("err = %v, want the original error unchanged", err)
		}
	})
}

// The probe costs an ssh round trip, so it runs ONLY for the ambiguous code.
func TestClassifyDialFailure_OtherCodes_DoNotProbe(t *testing.T) {
	cause := errors.New("dial failed")
	asked := stubProbeDestBatch(t, remoteinstall.Probe{}, nil)

	if err := classifyDialFailure("gpu01", fakeLink{exitCode: 127}, cause); !errors.Is(err, tui.ErrRemoteQuilMissing) {
		t.Errorf("127: err = %v, want ErrRemoteQuilMissing", err)
	}
	if err := classifyDialFailure("gpu01", fakeLink{exitCode: 1, established: true}, cause); err != cause {
		t.Errorf("established exit 1: err = %v, want unchanged", err)
	}
	if err := classifyDialFailure("gpu01", fakeLink{exitCode: transport.ExitSSHOwnFailure}, cause); err != cause {
		t.Errorf("255: err = %v, want unchanged", err)
	}
	if err := classifyDialFailure("gpu01", nil, cause); err != cause {
		t.Errorf("nil link: err = %v, want unchanged", err)
	}
	if len(*asked) != 0 {
		t.Errorf("probed %v for an unambiguous code", *asked)
	}
}

// Both production call sites must hand classifyDialFailure the destination
// they DIALLED — a probe of any other host answers the wrong question.
func TestDialCallSites_ExitOne_ProbeTheirOwnDestination(t *testing.T) {
	asReleaseBuild(t, "1.0.0")
	dialDead := func(t *testing.T) {
		var order []string
		client := clientRecordingClose(t, &order)
		stubDial(t, func(context.Context, config.Config, string, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error) {
			return client, fakeLink{exitCode: 1}, nil
		})
	}

	t.Run("dialExtra", func(t *testing.T) {
		dialDead(t)
		asked := stubProbeDestBatch(t, remoteinstall.Probe{OS: "windows"}, nil)
		_, err := dialExtra(config.Config{}, config.Destination{Dest: "win01"})()
		if !errors.Is(err, tui.ErrRemoteQuilMissing) {
			t.Errorf("err = %v, want ErrRemoteQuilMissing", err)
		}
		if len(*asked) != 1 || (*asked)[0] != "win01" {
			t.Errorf("probed %v, want [win01]", *asked)
		}
	})

	t.Run("dialMCPHost", func(t *testing.T) {
		dialDead(t)
		asked := stubProbeDestBatch(t, remoteinstall.Probe{OS: "windows"}, nil)
		_, err := dialMCPHost(config.Config{}, config.Destination{Dest: "win02"})
		if !errors.Is(err, tui.ErrRemoteQuilMissing) {
			t.Errorf("err = %v, want ErrRemoteQuilMissing", err)
		}
		if len(*asked) != 1 || (*asked)[0] != "win02" {
			t.Errorf("probed %v, want [win02]", *asked)
		}
	})
}
