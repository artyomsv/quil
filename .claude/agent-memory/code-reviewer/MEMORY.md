# Code Reviewer Memory Index

- [feedback_env_interpolation.md](feedback_env_interpolation.md) — security feedback: pass version strings via `env:` blocks, not raw `${{ }}` interpolation in run scripts
- [gofmt CRLF check](project_gofmt_crlf_check.md) — `gofmt -l` in the container flags every file (CRLF tree); strip `\r` and diff against HEAD before reporting drift
