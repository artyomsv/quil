package daemon

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// Every *_req type the protocol defines must answer an unreadable payload
// from a hello'd conn — with its own response or an error reply — never with
// silence. The list is read from protocol.go itself so a new request type is
// covered the day it is added.
func TestHandleMessage_EveryReqType_AnswersABadPayload(t *testing.T) {
	types := reqTypesFromProtocolSource(t) // go/parser over every non-test ../ipc/*.go: every Msg* const whose value ends in "_req"
	// A parse regression (a glob that stops matching, a broken path) must not
	// silently shrink this to an empty list and pass every remaining
	// sub-test vacuously — assert two request types from TWO DIFFERENT files
	// are actually present: version_req from protocol.go, and
	// create_from_template_req from template.go, which this glob is what
	// makes reachable at all (an earlier version parsed only protocol.go and
	// hello.go by name, so template.go's own *_req types were never covered).
	requireReqType(t, types, ipc.MsgVersionReq)
	requireReqType(t, types, ipc.MsgCreateFromTemplateReq)
	_, client := mcpTestDaemon(t)
	roundTrip(t, client, ipc.MsgHello, ipc.MsgHelloResp, ipc.HelloPayload{Kind: "script", Proto: 1, PID: os.Getpid()})
	for _, typ := range types {
		if reason, skip := skipBadPayload[typ]; skip {
			t.Logf("skip %s: %s", typ, reason)
			continue
		}
		t.Run(typ, func(t *testing.T) {
			msg := &ipc.Message{Type: typ, ID: "bad-" + typ, Payload: json.RawMessage(`"not an object"`)}
			if err := client.Send(msg); err != nil {
				t.Fatal(err)
			}
			waitFrameWithID(t, client, msg.ID, 5*time.Second) // any type, same ID
		})
	}
}

// skipBadPayload lists request types whose handler ignores its payload AND
// has a side effect a test must not trigger. Every entry needs its reason.
var skipBadPayload = map[string]string{
	// handleUpdateCheckReq takes no conn/msg at all (dispatched as
	// d.handleUpdateCheckReq(), no arguments) — it is fire-and-forget by
	// design, like client_hello, and nothing could ever answer this
	// request's id, good payload or bad. When update checks are enabled it
	// also triggers a real network call to check for a new release.
	ipc.MsgUpdateCheckReq: "fire-and-forget by design (handler takes no conn/msg) and would hit the network when update checks are enabled",
}

// requireReqType fails the test if want is absent from got — the guard
// against reqTypesFromProtocolSource silently shrinking to an empty or
// partial list and every sub-test above passing vacuously over nothing.
func requireReqType(t *testing.T, got []string, want string) {
	t.Helper()
	for _, typ := range got {
		if typ == want {
			return
		}
	}
	t.Fatalf("reqTypesFromProtocolSource did not find %q in %v", want, got)
}

// reqTypesFromProtocolSource parses every non-test ../ipc/*.go file with
// go/parser and returns every Msg* const whose string value ends in "_req" —
// a source-derived list so a new request type, in a new file, is covered the
// day it is added, rather than the day someone remembers to update a
// hand-typed file list here. A fixed two-file list (protocol.go, hello.go)
// silently missed create_from_template_req in template.go — the glob is what
// makes a new *_req file self-covering.
func reqTypesFromProtocolSource(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../ipc/*.go")
	if err != nil {
		t.Fatalf("glob ../ipc/*.go: %v", err)
	}
	var out []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Msg") {
						continue
					}
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if strings.HasSuffix(val, "_req") {
						out = append(out, val)
					}
				}
			}
		}
	}
	return out
}

// waitFrameWithID reads frames until one with the given ID arrives (any
// type), or fails the test once the read deadline trips.
//
// The deadline is on the socket itself (SetReadDeadline), not a bare
// time.After checked between reads — Receive blocks with no timeout of its
// own, so only a real read deadline can unpark one that never arrives, which
// is exactly the failure this helper exists to catch.
func waitFrameWithID(t *testing.T, client *ipc.Client, id string, timeout time.Duration) *ipc.Message {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	defer client.SetReadDeadline(time.Time{})
	for {
		resp, err := client.Receive()
		if err != nil {
			t.Fatalf("no frame arrived for id %s: %v", id, err)
		}
		if resp.ID == id {
			return resp
		}
	}
}
