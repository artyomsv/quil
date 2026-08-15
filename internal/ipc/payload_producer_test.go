package ipc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewMessage_IsTheOnlyPayloadProducer pins the invariant the whole encode
// fast path rests on.
//
// appendEnvelope writes Message.Payload into the envelope VERBATIM. That is
// only sound because every payload is compact, HTML-escaped, valid JSON — which
// holds because NewMessage marshals it, and NewMessage is the only production
// site that sets the field. payloadInlinable's json.Valid call is the backstop
// for validity, but not for compactness or HTML escaping, so a hand-built
// payload can still produce a frame that differs byte-for-byte from what every
// previous version of Quil sent.
//
// The design document listed this test as required and it was not written; a
// code review caught the omission. Prose in a comment does not fail a build.
//
// Scope note: this deliberately walks the whole module rather than one package.
// The invariant is about who may CONSTRUCT a Message anywhere in the repo, so a
// package-local check would pass while cmd/quil quietly broke it.
func TestNewMessage_IsTheOnlyPayloadProducer(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	type violation struct {
		pos  string
		expr string
	}
	var found []violation

	for _, dir := range []string{"cmd", "internal"} {
		walkErr := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// Test files legitimately hand-build payloads to exercise the
			// fallback and the decline paths; the invariant is about
			// production code.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// protocol.go is where NewMessage lives — the one sanctioned site.
			if filepath.Base(path) == "protocol.go" && strings.HasSuffix(filepath.Dir(path), "ipc") {
				return nil
			}

			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isMessageType(lit.Type) {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "Payload" {
						rel, _ := filepath.Rel(root, path)
						found = append(found, violation{
							pos:  filepath.ToSlash(rel) + ":" + fset.Position(kv.Pos()).String()[strings.LastIndex(fset.Position(kv.Pos()).String(), ":")+1:],
							expr: "Message{... Payload: ...}",
						})
					}
				}
				return true
			})
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", dir, walkErr)
		}
	}

	if len(found) > 0 {
		for _, v := range found {
			t.Errorf("production code sets Message.Payload directly at %s (%s)", v.pos, v.expr)
		}
		t.Fatal("Message.Payload must be set only by NewMessage, which marshals it.\n" +
			"appendEnvelope inlines the payload verbatim and assumes it is compact,\n" +
			"HTML-escaped, valid JSON. A hand-built payload breaks byte-identity with\n" +
			"every previously shipped version. Route it through NewMessage, or if this\n" +
			"site genuinely must bypass it, widen this test deliberately and say why.")
	}
}

// isMessageType matches both `Message{...}` (inside package ipc) and
// `ipc.Message{...}` (everywhere else).
func isMessageType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Message"
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		return ok && pkg.Name == "ipc" && t.Sel.Name == "Message"
	}
	return false
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod. Tests run in their package directory, so this is two levels
// up today — derived rather than hardcoded so moving the package does not
// silently turn this test into a no-op over an empty tree.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the test's working directory")
		}
		dir = parent
	}
}
