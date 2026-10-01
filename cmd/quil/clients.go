package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// tokenRequestTimeout bounds a wait for the daemon's answer. An older daemon
// has no handler and, for a conn that never said hello, answers nothing. A
// var so TestTokenRequest_OldDaemonTimesOut runs the real path quickly.
var tokenRequestTimeout = 5 * time.Second

var errDaemonDidNotAnswer = errors.New("the daemon did not answer — it may be older than this quil; restart it with `quil daemon restart`")

// tokenRequestFn is the seam the command tests stub; tokenDialFn is the one
// TestTokenRequest_OldDaemonTimesOut swaps so the real sendTokenRequest talks
// to a silent fake instead of the local daemon.
var (
	tokenRequestFn = sendTokenRequest
	tokenDialFn    = localClientForCommand
)

func handleClients() { handleClientsArgs(os.Args) }

// handleClientsArgs dispatches `quil clients token create|list|revoke`.
// Refused under --remote and --connect, like `quil daemon`: token management
// is local-socket only.
func handleClientsArgs(args []string) {
	if remoteMode() {
		fmt.Fprintf(os.Stderr, "quil clients: not available with --remote or --connect (target: %s)\n"+
			"Token management talks to the LOCAL daemon only; run it on that machine.\n", remoteDest)
		exitFn(1)
		return
	}
	if len(args) < 4 || args[2] != "token" {
		fmt.Fprintln(os.Stderr, "usage: quil clients token [create|list|revoke]")
		exitFn(1)
		return
	}
	var code int
	switch args[3] {
	case "create":
		code = runTokenCreate(args[4:], os.Stdout, os.Stderr)
	case "list":
		code = runTokenList(args[4:], os.Stdout, os.Stderr)
	case "revoke":
		code = runTokenRevoke(args[4:], os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "unknown token command: %s\n", args[3])
		code = 1
	}
	exitFn(code)
}

func runTokenCreate(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("quil clients token create", flag.ContinueOnError)
	fs.SetOutput(errOut)
	name := fs.String("name", "", "token name, 1-32 characters")
	rights := fs.String("rights", string(clientauth.LevelStandard), "read-only | standard | full")
	expires := fs.String("expires", clientauth.DefaultExpiry, "<N>d or never")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := clientauth.ValidName(*name); err != nil {
		fmt.Fprintf(errOut, "--name: %v\n", err)
		return 2
	}
	if _, err := clientauth.ParseLevel(*rights); err != nil {
		fmt.Fprintf(errOut, "--rights: %v\n", err)
		return 2
	}
	if _, _, err := clientauth.ParseExpiry(*expires); err != nil {
		fmt.Fprintf(errOut, "--expires: %v\n", err)
		return 2
	}
	resp, err := tokenRequestFn(ipc.MsgTokenCreateReq, ipc.TokenCreateReqPayload{Name: *name, Rights: *rights, Expires: *expires})
	if err != nil {
		fmt.Fprintf(errOut, "quil clients token create: %v\n", err)
		return 1
	}
	var p ipc.TokenCreateRespPayload
	if err := resp.DecodePayload(&p); err != nil || p.Error != "" {
		fmt.Fprintf(errOut, "quil clients token create: %s\n", orErr(p.Error, err))
		return 1
	}
	fmt.Fprintf(out, "Created token %q (id %s, rights %s, expires %s).\n", p.Name, p.ID, p.Rights, orNever(p.Expires))
	fmt.Fprintln(out, "Store it now — it is not shown again:")
	fmt.Fprintln(out, p.Token)
	fmt.Fprintln(out, "Use it with:  QUIL_TOKEN=<token> quil --connect <addr>   (or --token-file <path>)")
	return 0
}

func runTokenList(_ []string, out, errOut io.Writer) int {
	resp, err := tokenRequestFn(ipc.MsgTokenListReq, struct{}{})
	if err != nil {
		fmt.Fprintf(errOut, "quil clients token list: %v\n", err)
		return 1
	}
	var p ipc.TokenListRespPayload
	if err := resp.DecodePayload(&p); err != nil || p.Error != "" {
		fmt.Fprintf(errOut, "quil clients token list: %s\n", orErr(p.Error, err))
		return 1
	}
	if len(p.Tokens) == 0 {
		fmt.Fprintln(out, "No tokens. Create one with: quil clients token create --name <name>")
		return 0
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tRIGHTS\tCREATED\tEXPIRES\tLAST USED")
	for _, tk := range p.Tokens {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", tk.ID, tk.Name, tk.Rights,
			shortTime(tk.Created), orNever(shortTime(tk.Expires)), orDash(shortTime(tk.LastUsed)))
	}
	tw.Flush()
	return 0
}

func runTokenRevoke(args []string, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(errOut, "usage: quil clients token revoke <id|name>")
		return 2
	}
	resp, err := tokenRequestFn(ipc.MsgTokenRevokeReq, ipc.TokenRevokeReqPayload{Target: args[0]})
	if err != nil {
		fmt.Fprintf(errOut, "quil clients token revoke: %v\n", err)
		return 1
	}
	var p ipc.TokenRevokeRespPayload
	if err := resp.DecodePayload(&p); err != nil || p.Error != "" {
		fmt.Fprintf(errOut, "quil clients token revoke: %s\n", orErr(p.Error, err))
		return 1
	}
	fmt.Fprintf(out, "Revoked %s (%s); closed %d connection(s).\n", p.ID, p.Name, p.Closed)
	return 0
}

// sendTokenRequest talks to the LOCAL daemon (auto-started like the TUI does).
func sendTokenRequest(msgType string, payload any) (*ipc.Message, error) {
	client, err := tokenDialFn()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	msg, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		return nil, err
	}
	msg.ID = "clients-" + uuid.NewString()
	if err := client.Send(msg); err != nil {
		return nil, err
	}
	resp, err := client.ReceiveByID(msg.ID, tokenRequestTimeout)
	if err != nil {
		return nil, errDaemonDidNotAnswer
	}
	if resp.Type == ipc.MsgError {
		var p ipc.ErrorPayload
		_ = resp.DecodePayload(&p) // an undecodable error still refuses; the message is just empty
		return nil, fmt.Errorf("daemon refused: %s", p.Message)
	}
	return resp, nil
}

func localClientForCommand() (*ipc.Client, error) {
	sock := config.SocketPath()
	if c, err := ipc.NewClient(sock); err == nil {
		return c, nil
	}
	pid := startDaemon(true)
	if !waitForDaemonReady(sock, pid) {
		return nil, errors.New("daemon did not come up — check the daemon log (see 'quil daemon status')")
	}
	return ipc.NewClient(sock)
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func orNever(s string) string {
	if s == "" {
		return "never"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func orErr(msg string, err error) string {
	if msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
