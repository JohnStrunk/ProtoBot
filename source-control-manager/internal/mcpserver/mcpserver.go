// Package mcpserver serves the Drafting Table face as an MCP server over
// stdio. It is dual-era: a modern client gets revision 2026-07-28, the
// stateless protocol, and a client that opens with initialize gets the
// legacy revision 2025-11-25 for the life of the process. The results and
// the rules are the same in both eras.
package mcpserver

import (
	"context"
	"encoding/json"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/redhat-et/protobot/source-control-manager/internal/result"
	"github.com/redhat-et/protobot/source-control-manager/internal/scm"
)

// Name is the MCP server name.
const Name = "source-control-manager"

// toolListTTL is the cache hint of tools/list: the list changes only with
// the executable.
const toolListTTL = 24 * 60 * 60 * 1000

const instructions = "Governed Git and Git host operations for the ProtoBot Drafting Table. " +
	"Every operation derives the branch, the files, the message, and the pull request from the change set of the working tree; " +
	"no request names a ref, a path, a remote, or a repository. Commit and publish only on the user's explicit request."

var descriptions = map[string]string{
	scm.OpRepoState: "Read the project, the current branch and its change set, base_commit against the default head, " +
		"uncommitted change-set paths, the pull request, and the local change-set branches. " +
		"Fetches the canonical remote and fast-forwards the local default branch when that is a fast-forward.",
	scm.OpBranchInit: "Cut and check out the initialization branch <prefix>00001-project-init from the default branch, " +
		"before .protobot/ exists. Then run ears-manager project init with the same prefix and default branch.",
	scm.OpBranchResume: "Switch to the one local branch of a change set, on resume.",
	scm.OpCommit: "Commit the change set of the current branch, on the user's explicit request. " +
		"The SCM derives the file set, runs the pre-stage digest check, and writes the subject and the Change-Set trailer. " +
		"The optional body is prose; it holds no Change-Set: line, no closing keyword with an issue reference, and no @ mention.",
	scm.OpPublish: "Push the change-set branch to the canonical remote, without force and without tags, " +
		"and create or update its pull request with a body rendered from ears-manager output. Only on the user's explicit request.",
	scm.OpRefresh: "Merge the default branch into the change-set branch with a merge commit, and abort on a conflict. " +
		"The result names the new default head for change-set update --base-commit.",
}

func schema(properties string, required ...string) json.RawMessage {
	doc := `{"type":"object","properties":{` + properties + `},"additionalProperties":false`
	if len(required) > 0 {
		list, _ := json.Marshal(required)
		doc += `,"required":` + string(list)
	}
	return json.RawMessage(doc + `}`)
}

var schemas = map[string]json.RawMessage{
	scm.OpRepoState: schema(""),
	scm.OpBranchInit: schema(`"branch_prefix":{"type":"string","pattern":"^[a-z0-9][a-z0-9._-]*/$","default":"cs/"},` +
		`"default_branch":{"type":"string","default":"main"}`),
	scm.OpBranchResume: schema(`"change_set_id":{"type":"string","pattern":"^CS-[0-9]{5}$"}`, "change_set_id"),
	scm.OpCommit:       schema(`"body":{"type":"string","maxLength":2000}`),
	scm.OpPublish:      schema(""),
	scm.OpRefresh:      schema(""),
}

// NewServer builds the MCP server of the Drafting Table face.
func NewServer(srv *scm.Server, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
		SetCacheable: func(_ context.Context, req mcp.Request, c *mcp.Cacheable) {
			if _, ok := req.(*mcp.ListToolsRequest); ok {
				c.TTLMs = toolListTTL
				c.CacheScope = "public"
			}
		},
	})
	for _, op := range scm.Operations(result.FaceDraftingTable) {
		server.AddTool(&mcp.Tool{Name: op, Description: descriptions[op], InputSchema: schemas[op]}, handler(srv, op))
	}
	server.AddReceivingMiddleware(guard(srv))
	return server
}

// Serve runs the MCP server over the given streams until the client
// closes them.
func Serve(ctx context.Context, srv *scm.Server, version string, in io.ReadCloser, out io.WriteCloser) error {
	return NewServer(srv, version).Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}

func handler(srv *scm.Server, op string) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return call(srv, op, req.Params), nil
	}
}

// call runs one tool call. The result is the envelope's JSON as the text
// of the tool result, which is an error when ok is false.
func call(srv *scm.Server, op string, params *mcp.CallToolParamsRaw) *mcp.CallToolResult {
	inv := scm.Invocation{Face: result.FaceDraftingTable, Operation: op}
	if params != nil {
		inv.Trace = traceOf(params.Meta)
		if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
			if err := json.Unmarshal(params.Arguments, &inv.Args); err != nil {
				inv.ArgsNotObject = true
			}
		}
	}
	env := srv.Invoke(inv)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(env.JSON())}}, IsError: !env.OK}
}

// traceOf copies an OpenTelemetry trace context from a request's _meta.
func traceOf(meta mcp.Meta) any {
	var fields []string
	trace := map[string]any{}
	for _, key := range []string{"traceparent", "tracestate", "baggage"} {
		if value, ok := meta[key]; ok {
			trace[key] = value
			fields = append(fields, key)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return trace
}

// guard answers a call to a tool outside the face with the SCM's own
// UNAUTHORIZED_ACTION result, and returns the face's tools in their fixed
// order.
func guard(srv *scm.Server) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if ctr, ok := req.(*mcp.CallToolRequest); ok && ctr.Params != nil && !served(ctr.Params.Name) {
					return call(srv, ctr.Params.Name, ctr.Params), nil
				}
			}
			res, err := next(ctx, method, req)
			if method == "tools/list" && err == nil {
				if list, ok := res.(*mcp.ListToolsResult); ok {
					list.Tools = ordered(list.Tools)
				}
			}
			return res, err
		}
	}
}

func served(name string) bool {
	for _, op := range scm.Operations(result.FaceDraftingTable) {
		if op == name {
			return true
		}
	}
	return false
}

func ordered(tools []*mcp.Tool) []*mcp.Tool {
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	out := []*mcp.Tool{}
	for _, op := range scm.Operations(result.FaceDraftingTable) {
		if tool, ok := byName[op]; ok {
			out = append(out, tool)
		}
	}
	return out
}
