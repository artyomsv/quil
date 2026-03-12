//go:build windows

package main

import "syscall"

const _DETACHED_PROCESS = 0x00000008

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | _DETACHED_PROCESS,
	}
}
