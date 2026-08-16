package changelog

import "testing"

// withCorpus points the package at a fixed corpus for the duration of one test.
// The embedded file changes on every release, so a test bound to its contents
// would fail on release commits.
//
// Mutates a package-level var: callers must not use t.Parallel.
func withCorpus(t *testing.T, data string) {
	t.Helper()
	corpusOverride = &corpus{releases: parseHighlights([]byte(data))}
	t.Cleanup(func() { corpusOverride = nil })
}
