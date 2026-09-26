package remoteinstall

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestParseWindowsProbe(t *testing.T) {
	out := "Windows PowerShell banner noise\r\n__quil_probe_win__\r\n" +
		"C:\\Users\\Jürgen Müller\\AppData\\Local\r\nwindows\r\nAMD64\r\n" +
		"C:\\Users\\Jürgen Müller\\AppData\\Local\\Programs\\quil\\quil.exe\r\nrw\r\n-\r\n"
	p, err := ParseWindowsProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	if p.OS != "windows" || p.Platform.String() != "windows/amd64" || p.Shell != ShellCmd ||
		p.Home != `C:\Users\Jürgen Müller\AppData\Local` || !p.ExistingDirWritable ||
		p.ExistingPath != `C:\Users\Jürgen Müller\AppData\Local\Programs\quil\quil.exe` {
		t.Errorf("got %+v", p)
	}
}

func TestParseWindowsProbe_Refusals(t *testing.T) {
	base := []string{"__quil_probe_win__", `C:\Users\a\AppData\Local`, "windows", "AMD64", "-", "-", "-"}
	cases := map[string]func([]string){
		"arm64":         func(l []string) { l[3] = "ARM64" },
		"UNC home":      func(l []string) { l[1] = `\\srv\u` },
		"quote in quil": func(l []string) { l[4] = `C:\a'b\quil.exe`; l[5] = "rw" },
		"odd shell":     func(l []string) { l[6] = `C:\tools\fish.exe` },
		"short":         func(l []string) {},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			l := append([]string(nil), base...)
			mut(l)
			if name == "short" {
				l = l[:4]
			}
			if _, err := ParseWindowsProbe(strings.Join(l, "\n")); err == nil {
				t.Errorf("accepted %q", l)
			}
		})
	}
}

func TestShellFromDefault(t *testing.T) {
	for in, want := range map[string]string{
		"-": ShellCmd, "": ShellCmd, `C:\Windows\System32\cmd.exe`: ShellCmd,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: ShellPowerShell,
		`C:\Program Files\PowerShell\7\pwsh.exe`:                    ShellPowerShell,
		`C:\Program Files\Git\bin\bash.exe`:                         ShellPOSIX,
	} {
		if got, err := ShellFromDefault(in); err != nil || got != want {
			t.Errorf("ShellFromDefault(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ShellFromDefault(`C:\x\nu.exe`); err == nil {
		t.Error("unknown default shell accepted")
	}
}

// answer is one canned reply; Task 11's tests reuse it.
type answer struct {
	prefix string
	code   int
	stdout string
}

// scriptedRunner answers by command prefix.
type scriptedRunner struct {
	answers []answer
	ran     []string
}

func (s *scriptedRunner) Run(_ context.Context, cmd string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	s.ran = append(s.ran, cmd)
	for _, a := range s.answers {
		if strings.HasPrefix(cmd, a.prefix) {
			_, _ = io.WriteString(stdout, a.stdout)
			return a.code, nil
		}
	}
	return 1, nil
}

const winProbeOut = "__quil_probe_win__\nC:\\Users\\a\\AppData\\Local\nwindows\nAMD64\n-\n-\n-\n"

func TestRunProbe_NoShNoSentinel_SwitchesToWindows(t *testing.T) {
	r := &scriptedRunner{}
	r.answers = append(r.answers,
		answer{"sh -s", 1, ""},
		answer{"powershell.exe", 0, winProbeOut})
	p, err := RunProbe(context.Background(), r)
	if err != nil || p.OS != "windows" {
		t.Fatalf("got %+v, %v (ran %q)", p, err, r.ran)
	}
}

func TestRunProbe_SSHFailure255_DoesNotSwitch(t *testing.T) {
	r := &scriptedRunner{}
	r.answers = append(r.answers, answer{"sh -s", 255, ""})
	if _, err := RunProbe(context.Background(), r); err == nil {
		t.Fatal("255 produced a probe")
	}
	if len(r.ran) != 1 {
		t.Errorf("ran a second probe after ssh's own failure: %q", r.ran)
	}
}

func TestRunProbe_GitBashUname_SwitchesToWindows(t *testing.T) {
	r := &scriptedRunner{}
	r.answers = append(r.answers,
		answer{"sh -s", 0, "__quil_probe__\n/c/Users/a\nMINGW64_NT-10.0-19045\nx86_64\n-\n-\n"},
		answer{"powershell.exe", 0, winProbeOut})
	if p, err := RunProbe(context.Background(), r); err != nil || p.OS != "windows" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestRunProbe_POSIXAnswer_NeverRunsWindowsProbe(t *testing.T) {
	r := &scriptedRunner{}
	r.answers = append(r.answers, answer{
		"sh -s", 0, "__quil_probe__\n/home/a\nLinux\nx86_64\n-\n-\n"})
	p, err := RunProbe(context.Background(), r)
	if err != nil || p.OS != "" || len(r.ran) != 1 {
		t.Fatalf("got %+v, %v, ran %q", p, err, r.ran)
	}
}
