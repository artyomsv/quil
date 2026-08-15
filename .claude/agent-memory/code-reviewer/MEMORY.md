# Code Reviewer Memory Index

- [feedback_env_interpolation.md](feedback_env_interpolation.md) — security feedback: pass version strings via `env:` blocks, not raw `${{ }}` interpolation in run scripts
- [gofmt CRLF check](project_gofmt_crlf_check.md) — `gofmt -l` in the container flags every file (CRLF tree); strip `\r` and diff against HEAD before reporting drift
- [Mutation-test without editing](project_mutation_testing_without_editing.md) — robocopy the tree to the scratchpad, mutate the copy, run the Docker toolchain with `-count=1`; verify before claiming "no test covers X"
- [Race hides throughput flakes](project_race_hides_throughput_flakes.md) — green `-race` does NOT clear an ipc producer/consumer test; instrumentation slows the producer. Run plain, repeatedly, vs the pre-change file
