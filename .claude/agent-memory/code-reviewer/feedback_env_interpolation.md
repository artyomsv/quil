---
name: env_interpolation_security
description: Pass untrusted values via env: blocks in GitHub Actions run steps, not raw ${{ }} interpolation
type: feedback
---

Version strings and other potentially untrusted values must be passed through `env:` blocks in GitHub Actions `run:` steps rather than being interpolated directly with `${{ }}` inside shell script bodies.

**Why:** Script injection risk — if an attacker controls the value (e.g., via a crafted tag), raw interpolation can break out of the shell expression. Env vars are safe because the shell treats them as data, not code.

**How to apply:** Any time a `${{ }}` expression would appear inside the body of a `run:` block (not in `with:` or `if:` expressions), lift it to an `env:` block and reference it as `$ENV_VAR_NAME` in the script.
