# Code Reviewer Memory Index

- [feedback_env_interpolation.md](feedback_env_interpolation.md) — security feedback: pass version strings via `env:` blocks, not raw `${{ }}` interpolation in run scripts
- [gofmt CRLF check](project_gofmt_crlf_check.md) — `gofmt -l` in the container flags every file (CRLF tree); strip `\r` and diff against HEAD before reporting drift
- [Mutation-test without editing](project_mutation_testing_without_editing.md) — copy the tree to the scratchpad, mutate the copy, run the Docker toolchain with `-count=1`; verify before claiming "no test covers X". Includes the MSYS/tar path traps and the `dev.sh test` one-arg gotcha
- [Race hides throughput flakes](project_race_hides_throughput_flakes.md) — green `-race` does NOT clear an ipc producer/consumer test; instrumentation slows the producer. Run plain, repeatedly, vs the pre-change file
- [verify claims in container](project_verify_claims_in_container.md) — throwaway `zz_probe_test.go` + raw docker `go test -v -run`; `dev.sh test` passes neither flag
- [bubbletea key decoding](reference_bubbletea_key_decoding.md) — the parser is in `ultraviolet/decoder.go`, not bubbletea; ESC-prefix Meta yields `alt+M` (case in Code, no ModShift)
