package winjob

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeUTF16LE(t *testing.T, b []byte) string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte{0xFF, 0xFE}) {
		t.Fatalf("missing UTF-16LE BOM: % x", b[:4])
	}
	b = b[2:]
	if len(b)%2 != 0 {
		t.Fatalf("odd byte count %d", len(b))
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func validSpec() TaskSpec {
	return TaskSpec{
		UserSID:      "S-1-5-21-1-2-3-1001",
		LauncherPath: `E:\Tools\quil\quil-activate.exe`,
		QuildPath:    `E:\Tools\quil\quild.exe`,
		Home:         `C:\Users\a\.quil`,
	}
}

func TestLogonTaskXML_CarriesEveryRequiredField(t *testing.T) {
	raw, err := LogonTaskXML(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	x := decodeUTF16LE(t, raw)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<LogonTrigger>`,
		`<UserId>S-1-5-21-1-2-3-1001</UserId>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<Priority>5</Priority>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<Command>E:\Tools\quil\quil-activate.exe</Command>`,
		`<Arguments>start-daemon --quild &quot;E:\Tools\quil\quild.exe&quot; --home &quot;C:\Users\a\.quil&quot;</Arguments>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("task XML lacks %s\n%s", want, x)
		}
	}
	if strings.Count(x, "<UserId>S-1-5-21-1-2-3-1001</UserId>") != 2 {
		t.Errorf("want the SID in both the trigger and the principal")
	}
}

func TestLogonTaskXML_TrailingBackslashCannotEscapeTheQuote(t *testing.T) {
	s := validSpec()
	s.Home = `C:\Users\a\.quil\`
	raw, err := LogonTaskXML(s)
	if err != nil {
		t.Fatal(err)
	}
	if x := decodeUTF16LE(t, raw); !strings.Contains(x, `--home &quot;C:\Users\a\.quil&quot;`) {
		t.Errorf("trailing backslash survived into the argument:\n%s", x)
	}
}

func TestLogonTaskXML_EscapesXMLMetacharacters(t *testing.T) {
	s := validSpec()
	s.Home = `C:\Users\a&b<c>\.quil`
	raw, err := LogonTaskXML(s)
	if err != nil {
		t.Fatal(err)
	}
	x := decodeUTF16LE(t, raw)
	if !strings.Contains(x, `a&amp;b&lt;c&gt;`) {
		t.Errorf("XML metacharacters not escaped:\n%s", x)
	}
}

func TestLogonTaskXML_Refusals(t *testing.T) {
	cases := map[string]func(*TaskSpec){
		"quote in home":        func(s *TaskSpec) { s.Home = `C:\a"b` },
		"quote in quild":       func(s *TaskSpec) { s.QuildPath = `C:\a"b\quild.exe` },
		"relative launcher":    func(s *TaskSpec) { s.LauncherPath = `quil-activate.exe` },
		"UNC quild":            func(s *TaskSpec) { s.QuildPath = `\\srv\share\quild.exe` },
		"empty SID":            func(s *TaskSpec) { s.UserSID = "" },
		"control char in home": func(s *TaskSpec) { s.Home = "C:\\a\nb" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			s := validSpec()
			mut(&s)
			if _, err := LogonTaskXML(s); err == nil {
				t.Errorf("LogonTaskXML accepted %+v", s)
			}
		})
	}
}
