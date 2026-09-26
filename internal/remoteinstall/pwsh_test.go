package remoteinstall

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestEncodePowerShell_RoundTripsAndStaysSmall(t *testing.T) {
	cmd := EncodePowerShell(windowsProbeScript)
	const prefix = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "
	if !strings.HasPrefix(cmd, prefix) {
		t.Fatalf("prefix: %q", cmd[:60])
	}
	if len(cmd) > 8000 {
		t.Errorf("encoded probe is %d chars; cmd.exe's limit is 8191", len(cmd))
	}
	for _, r := range cmd {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/= .-", r) {
			t.Fatalf("encoded command contains %q, which a shell may interpret", r)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cmd, prefix))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	decoded := string(utf16.Decode(u))
	if !strings.Contains(decoded, "__quil_probe_win__") || !strings.Contains(decoded, "0x50000116") {
		t.Errorf("decoded script lost content:\n%s", decoded)
	}
}
