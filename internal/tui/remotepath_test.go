package tui

import "testing"

func TestDisplayBase(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users\a\proj`:           "proj",
		`C:\Users\a\proj\`:          "proj",
		`C:/Users/a/proj`:           "proj",
		`C:\`:                       `C:\`,
		`c:\`:                       `c:\`,
		`\\srv\share\dir`:           "dir",
		`E:\Projects\Jürgen Müller`: "Jürgen Müller",
		"/home/a/proj":              "proj",
		"/":                         "/",
		"":                          ".",
		`/home/a/we\ird`:            `we\ird`,
	} {
		if got := displayBase(in); got != want {
			t.Errorf("displayBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsWindowsPath(t *testing.T) {
	for in, want := range map[string]bool{
		`C:\x`: true, `c:/x`: true, `\\srv\s`: true,
		"/home/a": false, "relative": false, `C:`: false, "": false,
	} {
		if got := isWindowsPath(in); got != want {
			t.Errorf("isWindowsPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPathEqual_WindowsShapedPathsFoldOnAnyClient(t *testing.T) {
	if !pathEqual(`C:\Users\A\Proj`, `c:/users/a/proj`) {
		t.Error("Windows-shaped paths did not compare equal")
	}
	if pathEqualCase("/home/A", "/home/a", false) {
		t.Error("POSIX paths folded case")
	}
}
