- **`./scripts/dev.sh build` now targets the host platform instead of always
  cross-compiling for Windows.** On macOS and Linux it writes `quil`, `quild`,
  `quil-dev`, `quild-dev`, `quil-debug` and `quild-debug` for that machine, and
  skips the Windows-only prologue — the ConPTY fetch, the `go-winres` icon
  resources and `quil-activate.exe`, all of which are behind `//go:build
  windows` and unusable in the resulting binaries. Before this, a non-Windows
  checkout ended up with seven executables that could not run on the machine
  that had just built them, and the unsigned `quil-activate.exe` among them was
  reliably quarantined by endpoint security. Set `QUIL_BUILD_GOOS=windows`
  (optionally with `QUIL_BUILD_GOARCH`) to get the old behaviour from any host;
  `cross` is unchanged and still emits every platform. `clean` is now driven off
  the same binary list as `build`, so it removes the suffix-less names too — it
  never did before.
