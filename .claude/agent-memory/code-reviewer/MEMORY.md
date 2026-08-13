# Code Reviewer Memory Index

- [feedback_env_interpolation.md](feedback_env_interpolation.md) — security feedback: pass version strings via `env:` blocks, not raw `${{ }}` interpolation in run scripts
- [gofmt CRLF check](project_gofmt_crlf_check.md) — `gofmt -l` in the container flags every file (CRLF tree); strip `\r` and diff against HEAD before reporting drift
- [verify claims in container](project_verify_claims_in_container.md) — throwaway `zz_probe_test.go` + raw docker `go test -v -run`; `dev.sh test` passes neither flag
- [bubbletea key decoding](reference_bubbletea_key_decoding.md) — the parser is in `ultraviolet/decoder.go`, not bubbletea; ESC-prefix Meta yields `alt+M` (case in Code, no ModShift)
