---
headline: Toast clicks keep their windowless handler across updates
---
- **`quil update` now installs `quil-activate.exe` alongside `quil.exe` and `quild.exe`.**
  The windowless handler that a Windows notification click runs joined the release
  archive with the desktop-toast feature but never joined the updater, which extracts a
  fixed list of binaries. An install that has only ever been upgraded in place therefore
  never received it, and `quil notify setup` silently registered its fallback —
  `quil.exe activate`, a console binary whose window takes the foreground on every click
  and then disappears. Re-extracting the release zip was the only way to get the helper.
  Releases published before the helper existed still stage and apply normally: it is
  installed when the archive carries one and skipped without complaint when it does not,
  so downgrading is unaffected. Only a helper the verified release manifest declares is
  installed, so it is covered by the same checksum gate as `quil` and `quild` rather than
  being trusted for sitting in the staging directory. A helper that cannot be written —
  pinned by a handler still running, blocked by antivirus — is reported, leaves no partial
  file behind, and never fails the update itself.
