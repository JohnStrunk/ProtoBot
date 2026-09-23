// Package cli is the command line of source-control-manager:
//
//	source-control-manager [--output human|json] <operation> [options]
//	source-control-manager serve --face drafting-table [--transport stdio|streamable-http]
//	source-control-manager --version
//
// The operations are repo-state, branch-init, branch-resume, commit,
// publish, refresh, and approved-merge. An option is its request field's
// name with "_" replaced by "-" and a trailing "_id" dropped, so
// change_set_id is --change-set. A CLI call gets the same checks as an MCP
// call; the CLI is a convenience for people and services, not a boundary.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/mcpserver"
	"github.com/redhat-et/protobot/source-control-manager/internal/result"
	"github.com/redhat-et/protobot/source-control-manager/internal/scm"
)

// Version is the version of the executable, the core, and the result
// schema. The build may set it with -ldflags "-X ...cli.Version=...".
var Version = "0.1.0-dev"

// Exit statuses.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `usage:
  source-control-manager [--output human|json] <operation> [options]
  source-control-manager serve --face drafting-table [--transport stdio|streamable-http]
  source-control-manager --version

operations:
  repo-state
  branch-init [--branch-prefix PREFIX] [--default-branch BRANCH]
  branch-resume --change-set CS-NNNNN
  commit [--body TEXT]
  publish
  refresh
  approved-merge --change-set CS-NNNNN
`

// Main runs the command line and returns the exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	output := "human"
	takeOutput := func(i int) (int, bool) {
		value, next, ok := optionValue(args, i)
		if !ok || (value != "human" && value != "json") {
			return i, false
		}
		output = value
		return next, true
	}
	i := 0
	for i < len(args) && isOption(args[i], "--output") {
		next, ok := takeOutput(i)
		if !ok {
			return usageError(stderr, "--output takes human or json")
		}
		i = next
	}
	if i >= len(args) {
		return usageError(stderr, "no operation")
	}
	command := args[i]
	i++
	switch command {
	case "--version":
		_, _ = fmt.Fprintf(stdout, "source-control-manager %s\n", Version)
		return exitOK
	case "--help", "-h", "help":
		_, _ = io.WriteString(stdout, usage)
		return exitOK
	case "serve":
		return serve(args[i:], stdin, stdout, stderr)
	}
	operation := strings.ReplaceAll(command, "-", "_")
	face, ok := scm.FaceOf(operation)
	if !ok {
		return usageError(stderr, "unknown operation "+quote(command))
	}
	fields := map[string]any{}
	for i < len(args) {
		if isOption(args[i], "--output") {
			next, ok := takeOutput(i)
			if !ok {
				return usageError(stderr, "--output takes human or json")
			}
			i = next
			continue
		}
		if args[i] == "--help" || args[i] == "-h" {
			_, _ = io.WriteString(stdout, usage)
			return exitOK
		}
		if !strings.HasPrefix(args[i], "--") || len(args[i]) == 2 {
			return usageError(stderr, "unexpected argument "+quote(args[i]))
		}
		name := strings.TrimPrefix(args[i], "--")
		name, inline, hasInline := strings.Cut(name, "=")
		field := fieldOf(operation, name)
		switch {
		case hasInline:
			fields[field] = inline
			i++
		case i+1 < len(args) && !strings.HasPrefix(args[i+1], "--"):
			fields[field] = args[i+1]
			i += 2
		default:
			fields[field] = true
			i++
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "source-control-manager: cannot read the current directory")
		return exitFailure
	}
	env := (&scm.Server{Dir: dir}).Invoke(scm.Invocation{Face: face, Operation: operation, Args: fields})
	if output == "json" {
		_, _ = stdout.Write(append(env.JSON(), '\n'))
	} else {
		writeHuman(env, stdout, stderr)
	}
	if env.OK {
		return exitOK
	}
	return exitFailure
}

// fieldOf maps an option name to its request field: "-" becomes "_", and
// an option that drops "_id" gets it back.
func fieldOf(operation, option string) string {
	field := strings.ReplaceAll(option, "-", "_")
	for _, known := range scm.Fields(operation) {
		if known == field || known == field+"_id" {
			return known
		}
	}
	return field
}

func isOption(arg, name string) bool { return arg == name || strings.HasPrefix(arg, name+"=") }

func optionValue(args []string, i int) (string, int, bool) {
	if _, value, ok := strings.Cut(args[i], "="); ok {
		return value, i + 1, true
	}
	if i+1 < len(args) {
		return args[i+1], i + 2, true
	}
	return "", i + 1, false
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

func usageError(stderr io.Writer, message string) int {
	_, _ = fmt.Fprintf(stderr, "source-control-manager: %s\n\n%s", message, usage)
	return exitUsage
}

// writeHuman prints a short summary: the outcome and the data on stdout
// for a success, and the error on stderr for a failure.
func writeHuman(env result.Envelope, stdout, stderr io.Writer) {
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(env.JSON(), &doc)
	var operation, outcome string
	_ = json.Unmarshal(doc["operation"], &operation)
	if env.OK {
		_ = json.Unmarshal(doc["outcome"], &outcome)
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", operation, outcome)
		_, _ = fmt.Fprintf(stdout, "%s\n", indent(doc["data"]))
		return
	}
	var failure struct {
		Code     string          `json:"code"`
		Message  string          `json:"message"`
		Details  json.RawMessage `json:"details"`
		Mutation string          `json:"mutation"`
		Retry    string          `json:"retry"`
	}
	_ = json.Unmarshal(doc["error"], &failure)
	_, _ = fmt.Fprintf(stderr, "error[%s]: %s\n", failure.Code, failure.Message)
	if len(failure.Details) > 2 {
		_, _ = fmt.Fprintf(stderr, "details: %s\n", indent(failure.Details))
	}
	_, _ = fmt.Fprintf(stderr, "mutation: %s\nretry: %s\n", failure.Mutation, failure.Retry)
}

func indent(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return string(raw)
	}
	return buf.String()
}

// serve starts the MCP face. The transport is fixed when the process
// starts, and no request changes it.
func serve(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	face, transport := "", "stdio"
	for i := 0; i < len(args); {
		switch {
		case isOption(args[i], "--face"):
			value, next, ok := optionValue(args, i)
			if !ok {
				return usageError(stderr, "--face takes a face")
			}
			face, i = value, next
		case isOption(args[i], "--transport"):
			value, next, ok := optionValue(args, i)
			if !ok {
				return usageError(stderr, "--transport takes stdio or streamable-http")
			}
			transport, i = value, next
		default:
			return usageError(stderr, "unexpected argument "+quote(args[i]))
		}
	}
	if face != result.FaceDraftingTable {
		return usageError(stderr, "serve takes --face drafting-table")
	}
	switch transport {
	case "stdio":
	case "streamable-http":
		// The hosted face behind the Gate is not built yet: it is out of
		// the scope of issue #160. It never listens, so it can never
		// serve a call without the Gate's verification key.
		_, _ = fmt.Fprintln(stderr, "source-control-manager: the hosted face (streamable-http behind the Gate) is not available in this build")
		return exitFailure
	default:
		return usageError(stderr, "--transport takes stdio or streamable-http")
	}
	dir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "source-control-manager: cannot read the current directory")
		return exitFailure
	}
	in := io.NopCloser(stdin)
	out := nopWriteCloser{stdout}
	if err := mcpserver.Serve(context.Background(), &scm.Server{Dir: dir}, Version, in, out); err != nil && err != io.EOF {
		_, _ = fmt.Fprintf(stderr, "source-control-manager: %v\n", err)
		return exitFailure
	}
	return exitOK
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
