---
name: uintptrescapes-com-helper
description: x/sys/windows LazyProc.Call is //go:uintptrescapes, but a hand-rolled COM vtable `call` helper is not — Go pointers passed through it are not kept alive
metadata:
  type: project
---

`golang.org/x/sys/windows` marks BOTH `(*Proc).Call` and `(*LazyProc).Call` with
`//go:uintptrescapes` (verified v0.46.0, `windows/dll_windows.go:156` and `:305`).
So `procFoo.Call(uintptr(unsafe.Pointer(&x)))` is SAFE — the directive forces `x`
to escape and stay alive for the whole call.

A hand-written dispatch helper does **not** inherit that. This repo's COM vtable
helper is the shape to watch:

```go
func call(obj uintptr, index int, args ...uintptr) uintptr {
	full := append([]uintptr{obj}, args...)          // provenance lost here
	ret, _, _ := syscall.SyscallN(vtbl(obj, index), full...)
	return ret
}
```

Two independent breaks: the helper carries no directive, so the caller's
`uintptr(unsafe.Pointer(&out))` keeps nothing alive; and rebuilding the args into
a new slice means even `SyscallN`'s own keepalive cannot apply, because that only
covers literal `uintptr(unsafe.Pointer(...))` argument expressions at the
`SyscallN` call site.

**Why:** `unsafe.Pointer`→`uintptr` is exactly the conversion escape analysis does
NOT treat as escaping — that is why the directive exists as an opt-in. The out-param
locals stay on the stack, and a stack copy triggered inside the helper (its prologue,
or the `append` allocation) leaves COM writing to a stale address. Nondeterministic
corruption, and `//go:build windows` files are never compiled by CI (Linux only), so
no test, no `go vet`, no CodeQL run will ever reach it.

**How to apply:** when reviewing any Win32/COM interop here (`internal/clipboard`,
`internal/pty`, `internal/notify`, `cmd/quil/*_windows.go`), check whether a Go
pointer crosses an ORDINARY Go function on its way to a syscall. Fix is one line —
`//go:uintptrescapes` immediately above the helper (blank line before the doc
comment, as x/sys does it). Note `internal/clipboard` is often cited as the house
pattern but never passes a Go pointer through `.Call` at all, so it is not
precedent for code that does. Also worth flagging separately: `GOOS=windows go vet
./...` costs nothing in CI and would catch the `unsafeptr` violations in these files.
