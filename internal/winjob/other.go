//go:build !windows

package winjob

import "errors"

// ErrUnsupported is returned by the Task Scheduler helpers off Windows.
var ErrUnsupported = errors.New("only supported on Windows")

func InKillOnCloseJob() (inJob, breakawayOK bool, err error) { return false, false, nil }
func UserHasInteractiveSession() (bool, error)               { return false, nil }
func InServiceSession() bool                                 { return false }
func AboveMedium() (bool, error)                             { return false, nil }
func CurrentUserSID() (string, error)                        { return "", ErrUnsupported }
func Schtasks(args []string, lowered bool) (string, error)   { return "", ErrUnsupported }
func TaskExists(name string) bool                            { return false }
func RunTask(name string) error                              { return ErrUnsupported }
