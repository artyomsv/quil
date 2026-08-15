---
name: project-verify-claims-in-container
description: How to run a throwaway probe test (with -run/-v) to check a review claim empirically — dev.sh test supports neither flag
metadata:
  type: project
---

To verify a review claim empirically (timing, `lipgloss.Width` behaviour, "would the
old code have failed this test"), write a throwaway `internal/<pkg>/zz_probe_test.go`,
run it with the raw docker line, then delete it:

```
docker run --rm -v "$(pwd -W):/src" -v quil-gomod:/go/pkg/mod \
  -v quil-gocache:/root/.cache/go-build -w //src golang:1.25-alpine \
  go test -v -timeout 300s -run 'TestZZProbe' ./internal/tui/
```

**Why:** `./scripts/dev.sh test <pkg>` passes neither `-run` nor `-v`, so `t.Logf`
output is invisible and a probe would run the whole suite. The docker line is
`DOCKER_RUN` from `scripts/dev.sh` verbatim, so it reuses the same warm module and
build caches.

**How to apply:** prefer this over mutating tracked source (other agents share the
tree). To answer "is this new test capable of failing", copy the OLD implementation
into the probe file as `oldFoo` and sweep it over the new test's own sample set —
that proves the test's power without a `git checkout` window. Always `rm` the probe
and confirm `git status` is clean before reporting. Verify before asserting: a
plausible complexity argument was wrong here by three orders of magnitude in both
directions. See [[project-gofmt-crlf-check]].
