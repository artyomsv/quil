package remoteinstall

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// powershellPrefix runs Windows PowerShell 5.1 — always present, unlike pwsh
// — with a base64 UTF-16LE script. The whole command line contains no
// character cmd.exe or PowerShell interprets, so it runs identically under
// either default ssh shell. cmd.exe caps a command line at 8191 characters;
// tests hold every encoded script under 8000.
const powershellPrefix = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "

// EncodePowerShell returns the full remote command for script.
//
// Blank lines and full-line `#` comments are dropped and indentation trimmed
// before encoding, because every byte costs 2.67 after UTF-16 and base64. The
// scripts this package embeds contain no here-strings, so trimming indentation
// cannot change a string literal.
//
// remote-probe.ps1's ACL mask 0x50000116 = WriteData 0x2 | AppendData 0x4 |
// WriteExtendedAttributes 0x10 | WriteAttributes 0x100 | GenericAll 0x10000000
// | GenericWrite 0x40000000; Modify and FullControl include WriteData.
func EncodePowerShell(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		b.WriteString(t)
		b.WriteString("\n")
	}
	u := utf16.Encode([]rune(b.String()))
	raw := make([]byte, 2*len(u))
	for i, v := range u {
		raw[2*i] = byte(v)
		raw[2*i+1] = byte(v >> 8)
	}
	return powershellPrefix + base64.StdEncoding.EncodeToString(raw)
}
