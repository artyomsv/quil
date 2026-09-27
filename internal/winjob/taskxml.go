package winjob

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// TaskSpec is everything the logon task needs. Paths are absolute Windows
// drive paths; UserSID is the registering user's SID string.
type TaskSpec struct {
	UserSID      string
	LauncherPath string // quil-activate.exe
	QuildPath    string
	Home         string // QUIL_HOME the daemon serves
}

// LogonTaskXML renders the Task Scheduler definition, UTF-16LE with a BOM —
// the only encoding `schtasks /Create /XML` accepts.
//
// XML rather than schtasks flags because only XML can set the fields that make
// the task safe to run a daemon: ExecutionTimeLimit PT0S (the default 72 h
// would kill the daemon after three days) and Priority 5 (the default 7 is
// below normal, and every pane would inherit it).
//
// The user is named by SID, not DOMAIN\user: inside an ssh session
// %USERDOMAIN% reads WORKGROUP (measured, issue #236 probe 13).
func LogonTaskXML(s TaskSpec) ([]byte, error) {
	if s.UserSID == "" {
		return nil, errors.New("logon task: empty user SID")
	}
	for what, p := range map[string]string{
		"launcher": s.LauncherPath, "quild": s.QuildPath, "home": s.Home,
	} {
		if err := checkTaskPath(what, p); err != nil {
			return nil, err
		}
	}
	args := fmt.Sprintf(`start-daemon --quild "%s" --home "%s"`,
		quotableArg(s.QuildPath), quotableArg(s.Home))

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo><Description>Starts the Quil daemon at logon.</Description></RegistrationInfo>\n")
	b.WriteString("  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>" + esc(s.UserSID) + "</UserId></LogonTrigger></Triggers>\n")
	b.WriteString(`  <Principals><Principal id="Author"><UserId>` + esc(s.UserSID) +
		"</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	b.WriteString("    <Priority>5</Priority>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString(`  <Actions Context="Author"><Exec><Command>` + esc(s.LauncherPath) +
		"</Command><Arguments>" + esc(args) + "</Arguments></Exec></Actions>\n")
	b.WriteString("</Task>\n")
	return utf16LEWithBOM(b.String()), nil
}

// checkTaskPath accepts only an absolute drive path with no quote and no
// control character. A quote would end the argument early; a UNC path would
// make every logon an SMB fetch with implicit authentication.
func checkTaskPath(what, p string) error {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
		return fmt.Errorf("logon task: %s %q is not an absolute drive path", what, p)
	}
	if strings.ContainsRune(p, '"') {
		return fmt.Errorf("logon task: %s %q contains a double quote", what, p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("logon task: %s contains a control character", what)
		}
	}
	return nil
}

// quotableArg drops trailing separators. CommandLineToArgvW reads `\"` as an
// escaped quote, so `--home "C:\x\"` would swallow the rest of the line. Same
// rule as internal/notify's quotableDir, restated here because that one is
// unexported and belongs to the toast registry.
func quotableArg(p string) string { return strings.TrimRight(p, `\/`) }

func esc(s string) string {
	var b bytes.Buffer
	// EscapeText also escapes '"' as &#34;; normalise to &quot; so the
	// output is readable when a human opens it in Task Scheduler.
	_ = xml.EscapeText(&b, []byte(s)) // writes to a bytes.Buffer: cannot fail
	return strings.ReplaceAll(b.String(), "&#34;", "&quot;")
}

func utf16LEWithBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(u))
	out[0], out[1] = 0xFF, 0xFE
	for i, v := range u {
		out[2+2*i] = byte(v)
		out[3+2*i] = byte(v >> 8)
	}
	return out
}
