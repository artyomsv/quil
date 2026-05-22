//go:build windows

package opencodehook

// Windows has no O_NOFOLLOW equivalent in syscall, and creating NTFS symlinks
// already requires elevated privileges so the attack surface is narrower. The
// portable constant is 0 (no-op flag) — we still get the rest of the
// hardening (atomic write, mode 0600, ValidateQuilDir).
const oNoFollow = 0
