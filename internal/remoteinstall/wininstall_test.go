package remoteinstall

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodePowerShell returns the script an EncodePowerShell command carries.
func decodePowerShell(t *testing.T, cmd string) string {
	t.Helper()
	if !strings.HasPrefix(cmd, powershellPrefix) {
		t.Fatalf("not an encoded PowerShell command: %.60q", cmd)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cmd, powershellPrefix))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// step is one canned reply of a seqRunner.
type step struct {
	prefix         string
	code           int
	stdout, stderr string
}

// seqRunner answers each command with the NEXT step, in order, and records
// the command and its stdin. A command that does not match its step's prefix
// fails the test: the install is a sequence, and a step run out of order is
// exactly the bug these tests exist for.
type seqRunner struct {
	t     *testing.T
	steps []step
	ran   []string
	stdin [][]byte
}

func (s *seqRunner) Run(_ context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	s.ran = append(s.ran, cmd)
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	s.stdin = append(s.stdin, in)
	i := len(s.ran) - 1
	if i >= len(s.steps) {
		s.t.Errorf("unexpected command #%d: %.80q", i, cmd)
		return 1, nil
	}
	st := s.steps[i]
	if !strings.HasPrefix(cmd, st.prefix) {
		s.t.Errorf("command #%d = %.80q, want prefix %q", i, cmd, st.prefix)
	}
	_, _ = io.WriteString(stdout, st.stdout)
	_, _ = io.WriteString(stderr, st.stderr)
	return st.code, nil
}

// A realistic worst case: a non-ASCII profile with spaces, 150 characters.
var longWinDir = func() string {
	d := `C:\Users\Jürgen Müller-Lüdenscheidt (x86) @corp\AppData\Local\Programs\quil`
	for len(d) < 150 {
		d = `C:\Users\` + strings.Repeat("x", 150-len(d)) + d[len(`C:\Users\`):]
	}
	return d
}()

const tarPath = `C:\Windows\System32\tar.exe`

var (
	hexA = strings.Repeat("ab", 32)
	hexB = strings.Repeat("cd", 32)
	hexC = strings.Repeat("ef", 32)
)

func winSource() Source {
	return Source{
		Archive:    []byte("TAR-GZ-BYTES"),
		SHA256:     strings.Repeat("01", 32),
		FileSHA256: map[string]string{"quil.exe": hexA, "quild.exe": hexB, "quil-activate.exe": hexC},
	}
}

func stagingFor(dir string) string {
	return winJoin(dir, ".quil-staging-"+strings.Repeat("0f", 16))
}

// The prepare script creates directories literally (New-Item -Path treats [ and
// ] as wildcards) and clears only stale staging dirs directly under the
// install dir.
func TestWindowsPrepareCommand_LiteralDirsAndScopedStagingCleanup(t *testing.T) {
	cmd, err := WindowsPrepareCommand(Target{OS: "windows", Dir: longWinDir})
	if err != nil {
		t.Fatal(err)
	}
	script := decodePowerShell(t, cmd)
	for _, want := range []string{
		"[IO.Directory]::CreateDirectory($dir) | Out-Null",
		"[IO.Directory]::CreateDirectory($st) | Out-Null",
		"Get-ChildItem -LiteralPath $dir -Directory -Filter '.quil-staging-*'",
		"Remove-Item -LiteralPath $_.FullName -Recurse -Force",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("prepare script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "New-Item") {
		t.Errorf("prepare script still uses New-Item:\n%s", script)
	}
	if strings.Contains(script, "-Recurse -Filter") || strings.Contains(script, "Get-ChildItem -Path") {
		t.Errorf("staging cleanup is not scoped to the install dir itself:\n%s", script)
	}
	// Cleanup must run before the new staging dir exists, or it deletes it.
	if strings.Index(script, "-Filter '.quil-staging-*'") > strings.Index(script, "CreateDirectory($st)") {
		t.Errorf("stale staging cleanup runs after the new staging dir is created:\n%s", script)
	}
}

func TestWindowsPrepareCommand(t *testing.T) {
	tgt := Target{OS: "windows", Dir: longWinDir}
	cmd, err := WindowsPrepareCommand(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd) > 8000 {
		t.Errorf("encoded prepare is %d chars; cmd.exe's limit is 8191", len(cmd))
	}
	script := decodePowerShell(t, cmd)
	if !strings.Contains(script, "$dir = '"+longWinDir+"'") || strings.Contains(script, "__DIR__") {
		t.Errorf("dir not substituted:\n%s", script)
	}
	for _, bad := range []string{`\\srv\s\quil`, `C:\a'b`, `C:\a&b`, `relative\quil`} {
		if _, err := WindowsPrepareCommand(Target{OS: "windows", Dir: bad}); err == nil {
			t.Errorf("accepted dir %q", bad)
		}
	}
}

func TestParsePrepareOutput(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\Users\Jürgen Müller\AppData\Local\Programs\quil`}
	good := stagingFor(tgt.Dir)
	out := func(staging, tar string) string {
		return "banner\r\n__quil_prepare__\r\n" + staging + "\r\n" + tar + "\r\n"
	}

	st, tr, err := ParsePrepareOutput(tgt, out(good, tarPath))
	if err != nil || st != good || tr != tarPath {
		t.Fatalf("valid: %q %q %v", st, tr, err)
	}
	// Get-ChildItem and Join-Path may hand back a different case than the
	// planner used; Windows paths are case-insensitive.
	lower := strings.ToLower(tgt.Dir) + `\.quil-staging-` + strings.Repeat("0f", 16)
	if _, _, err := ParsePrepareOutput(tgt, out(lower, tarPath)); err != nil {
		t.Errorf("case-different prefix refused: %v", err)
	}
	root := Target{OS: "windows", Dir: `D:\`}
	if _, _, err := ParsePrepareOutput(root, out(stagingFor(`D:\`), tarPath)); err != nil {
		t.Errorf("drive-root staging refused: %v", err)
	}

	refused := map[string]string{
		"outside the target": out(`C:\Temp\.quil-staging-`+strings.Repeat("0f", 16), tarPath),
		"wrong suffix":       out(winJoin(tgt.Dir, ".quil-staging-xyz"), tarPath),
		"upper-case hex":     out(winJoin(tgt.Dir, ".quil-staging-"+strings.Repeat("0F", 16)), tarPath),
		"trailing path":      out(good+`\..\..`, tarPath),
		"UNC tar":            out(good, `\\srv\share\tar.exe`),
		"quote in tar":       out(good, `C:\a'b\tar.exe`),
		"no sentinel":        good + "\n" + tarPath + "\n",
		"short":              "__quil_prepare__\n" + good + "\n",
		// LAST sentinel wins: a forged block echoed later must not be ignored
		// in favour of the real one before it.
		"forged last": out(good, tarPath) + out(`C:\Windows\.quil-staging-`+strings.Repeat("0f", 16), tarPath),
	}
	for name, o := range refused {
		if _, _, err := ParsePrepareOutput(tgt, o); err == nil {
			t.Errorf("%s: accepted %q", name, o)
		}
	}

	// And the other direction: an rc-file echo of a forged block BEFORE the
	// real one is ignored.
	forgedFirst := out(`C:\Windows`, tarPath) + out(good, tarPath)
	if st, _, err := ParsePrepareOutput(tgt, forgedFirst); err != nil || st != good {
		t.Errorf("forged earlier sentinel: %q %v", st, err)
	}
}

func TestWindowsExtractCommand(t *testing.T) {
	staging := stagingFor(`C:\Users\Jürgen Müller\AppData\Local\Programs\quil`)
	cmd, err := WindowsExtractCommand(ShellCmd, tarPath, staging)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"` + tarPath + `" -xf - -C "` + staging + `"`; cmd != want {
		t.Errorf("cmd:\n got %q\nwant %q", cmd, want)
	}
	cmd, err = WindowsExtractCommand(ShellPowerShell, tarPath, staging)
	if err != nil {
		t.Fatal(err)
	}
	if want := `& '` + tarPath + `' -xf - -C '` + staging + `'`; cmd != want {
		t.Errorf("powershell:\n got %q\nwant %q", cmd, want)
	}
	for _, bad := range []string{`\\srv\s`, `C:\a"b`} {
		if _, err := WindowsExtractCommand(ShellCmd, tarPath, bad); err == nil {
			t.Errorf("accepted staging %q", bad)
		}
	}
	if _, err := WindowsExtractCommand("fish", tarPath, staging); err == nil {
		t.Error("accepted an unknown shell")
	}
}

func TestWindowsFinalizeCommand(t *testing.T) {
	tgt := Target{OS: "windows", Dir: longWinDir}
	staging := stagingFor(longWinDir)
	cmd, err := WindowsFinalizeCommand(tgt, staging, winSource())
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd) > 8000 {
		t.Errorf("encoded finalize is %d chars; cmd.exe's limit is 8191", len(cmd))
	}
	script := decodePowerShell(t, cmd)
	want := "$want = @{'quil-activate.exe'='" + hexC + "';'quil.exe'='" + hexA + "';'quild.exe'='" + hexB + "'}"
	if !strings.Contains(script, want) {
		t.Errorf("hash table not in sorted literal form; want %q in:\n%s", want, script)
	}
	if !strings.Contains(script, "$st = '"+staging+"'") || !strings.Contains(script, "$dir = '"+longWinDir+"'") {
		t.Errorf("paths not substituted:\n%s", script)
	}
	if strings.Contains(script, "__") && !strings.Contains(script, "__quil_install__") {
		t.Errorf("a placeholder survived:\n%s", script)
	}
}

func TestWindowsFinalizeCommand_Refusals(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\q`}
	staging := stagingFor(`C:\q`)
	with := func(mut func(map[string]string)) Source {
		src := winSource()
		m := map[string]string{}
		for k, v := range src.FileSHA256 {
			m[k] = v
		}
		mut(m)
		src.FileSHA256 = m
		return src
	}
	cases := map[string]Source{
		"short hash":      with(func(m map[string]string) { m["quil.exe"] = "abc" }),
		"upper-case hash": with(func(m map[string]string) { m["quil.exe"] = strings.ToUpper(hexA) }),
		"quote in hash":   with(func(m map[string]string) { m["quil.exe"] = "'" + hexA[1:] }),
		"path in name":    with(func(m map[string]string) { m[`..\evil.exe`] = hexA }),
		"quote in name":   with(func(m map[string]string) { m["a'b.exe"] = hexA }),
		"not an exe":      with(func(m map[string]string) { m["quil.ps1"] = hexA }),
		"no quild.exe":    with(func(m map[string]string) { delete(m, "quild.exe") }),
		"no quil.exe":     with(func(m map[string]string) { delete(m, "quil.exe") }),
	}
	for name, src := range cases {
		if _, err := WindowsFinalizeCommand(tgt, staging, src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The finalize step deletes the staging dir recursively, so a staging
	// path that is not ours must never reach it.
	for _, bad := range []string{`C:\`, `C:\q`, `C:\Windows\.quil-staging-` + strings.Repeat("0f", 16), `\\srv\s`} {
		if _, err := WindowsFinalizeCommand(tgt, bad, winSource()); err == nil {
			t.Errorf("accepted staging %q", bad)
		}
	}
	if _, err := WindowsFinalizeCommand(Target{OS: "windows", Dir: `C:\a'b`}, staging, winSource()); err == nil {
		t.Error("accepted a dir with a quote")
	}
}

func winPrepareOut(dir string) string {
	return "__quil_prepare__\r\n" + stagingFor(dir) + "\r\n" + tarPath + "\r\n"
}

func TestPushWindows_ThreeStepsInOrder(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\Users\Jürgen Müller\AppData\Local\Programs\quil`}
	staging := stagingFor(tgt.Dir)
	r := &seqRunner{t: t, steps: []step{
		{prefix: powershellPrefix, stdout: winPrepareOut(tgt.Dir)},
		{prefix: `"` + tarPath + `"`},
		{prefix: powershellPrefix, stdout: "__quil_install__\r\n" + tgt.Dir + `\quil.exe` + "\r\n"},
	}}
	src := winSource()
	if err := Push(context.Background(), r, ShellCmd, tgt, src); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(r.ran) != 3 {
		t.Fatalf("ran %d commands, want 3", len(r.ran))
	}
	if s := decodePowerShell(t, r.ran[0]); !strings.Contains(s, "__quil_prepare__") {
		t.Errorf("first command is not the prepare step:\n%s", s)
	}
	if want := `"` + tarPath + `" -xf - -C "` + staging + `"`; r.ran[1] != want {
		t.Errorf("extract = %q, want %q", r.ran[1], want)
	}
	if s := decodePowerShell(t, r.ran[2]); !strings.Contains(s, "__quil_install__") || !strings.Contains(s, staging) {
		t.Errorf("third command is not the finalize step for %s:\n%s", staging, s)
	}
	// Only the tar step reads stdin; PowerShell cannot (Ruling R-10).
	if len(r.stdin[0]) != 0 || string(r.stdin[1]) != "TAR-GZ-BYTES" || len(r.stdin[2]) != 0 {
		t.Errorf("stdin = %q", r.stdin)
	}
}

func TestPushWindows_PrepareFails_StopsBeforeCopy(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\q`}
	r := &seqRunner{t: t, steps: []step{
		{prefix: powershellPrefix, code: 4, stderr: "this Windows has no tar.exe; Windows 10 version 1803 or later is required"},
	}}
	err := Push(context.Background(), r, ShellCmd, tgt, winSource())
	if err == nil || !strings.Contains(err.Error(), "prepare") || !strings.Contains(err.Error(), "no tar.exe") {
		t.Errorf("err = %v", err)
	}
	if len(r.ran) != 1 {
		t.Errorf("ran %d commands after a failed prepare", len(r.ran))
	}

	// Exit 0 with a report that does not parse stops just the same.
	r = &seqRunner{t: t, steps: []step{{prefix: powershellPrefix, stdout: "__quil_prepare__\nC:\\elsewhere\n" + tarPath + "\n"}}}
	if err := Push(context.Background(), r, ShellCmd, tgt, winSource()); err == nil || len(r.ran) != 1 {
		t.Errorf("unparseable prepare: err %v, ran %d", err, len(r.ran))
	}
}

func TestPushWindows_CopyFails_StopsBeforeFinalize(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\q`}
	r := &seqRunner{t: t, steps: []step{
		{prefix: powershellPrefix, stdout: winPrepareOut(tgt.Dir)},
		{prefix: `"` + tarPath + `"`, code: 1, stderr: "tar.exe: Error opening archive: Unrecognized archive format"},
	}}
	err := Push(context.Background(), r, ShellCmd, tgt, winSource())
	if err == nil || !strings.Contains(err.Error(), "copy the archive into") || !strings.Contains(err.Error(), "Unrecognized") {
		t.Errorf("err = %v", err)
	}
	if len(r.ran) != 2 {
		t.Errorf("ran %d commands, want 2", len(r.ran))
	}
}

func TestPushWindows_ChecksumFailure_Reported(t *testing.T) {
	tgt := Target{OS: "windows", Dir: `C:\q`}
	r := &seqRunner{t: t, steps: []step{
		{prefix: powershellPrefix, stdout: winPrepareOut(tgt.Dir)},
		{prefix: `"` + tarPath + `"`},
		{prefix: powershellPrefix, code: 3, stderr: "checksum mismatch: quil.exe"},
	}}
	err := Push(context.Background(), r, ShellCmd, tgt, winSource())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch: quil.exe") || !strings.Contains(err.Error(), `install into C:\q`) {
		t.Errorf("err = %v", err)
	}
}

func TestInstallLogonTask(t *testing.T) {
	ok := &scriptedRunner{}
	ok.answers = append(ok.answers, answer{`"C:\q\quil.exe" daemon install-logon`, 0, "registered"})
	if w, err := InstallLogonTask(context.Background(), ok, ShellCmd, `C:\q\quil.exe`); err != nil || w != "" {
		t.Errorf("success path: %q %v", w, err)
	}
	fail := &scriptedRunner{}
	fail.answers = append(fail.answers, answer{`"C:\q\quil.exe"`, 1, "install-logon: C:\\q\\quil-activate.exe is missing"})
	if w, err := InstallLogonTask(context.Background(), fail, ShellCmd, `C:\q\quil.exe`); err != nil || !strings.Contains(w, "quil-activate.exe is missing") {
		t.Errorf("failure path: warning %q err %v", w, err)
	}
	silent := &scriptedRunner{}
	silent.answers = append(silent.answers, answer{`"C:\q\quil.exe"`, 2, ""})
	if w, err := InstallLogonTask(context.Background(), silent, ShellCmd, `C:\q\quil.exe`); err != nil || !strings.Contains(w, "2") {
		t.Errorf("silent failure: warning %q err %v", w, err)
	}
	if _, err := InstallLogonTask(context.Background(), ok, ShellCmd, `\\srv\quil.exe`); err == nil {
		t.Error("accepted a UNC binary")
	}
}

// An in-place install adopts a directory the USER owns, so the finalize
// step's cleanup may delete only the files quil itself renamed aside there —
// never someone's own backup.old. The sweep's pattern is the one match in the
// script, anchored to the three names quil installs.
func TestFinalizeScript_SweepsOnlyQuilsOwnOldFiles(t *testing.T) {
	const narrow = `-match '^(quil|quild|quil-activate)\.exe\.old(\.\d+)?$'`
	if !strings.Contains(windowsFinalizeScript, narrow) {
		t.Errorf("finalize script lacks the anchored sweep %s", narrow)
	}
	if n := strings.Count(windowsFinalizeScript, "-match"); n != 1 {
		t.Errorf("finalize script has %d -match clauses, want only the anchored sweep", n)
	}
	if n := strings.Count(windowsFinalizeScript, `\.old`); n != 1 {
		t.Errorf("finalize script matches `\\.old` %d times, want only the anchored sweep", n)
	}
}
