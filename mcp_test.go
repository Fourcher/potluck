package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMCP is a minimal MCP server over stdio: two tools, one of which fails.
const fakeMCP = `
import json, sys
for line in sys.stdin:
    m = json.loads(line)
    if "id" not in m:
        continue
    r = {"jsonrpc": "2.0", "id": m["id"]}
    meth = m.get("method")
    if meth == "initialize":
        r["result"] = {"protocolVersion": m["params"]["protocolVersion"], "capabilities": {"tools": {}}, "serverInfo": {"name": "fake", "version": "1"}}
    elif meth == "tools/list":
        r["result"] = {"tools": [
            {"name": "add", "description": "Add two numbers", "inputSchema": {"type": "object", "properties": {"a": {"type": "number"}, "b": {"type": "number"}}}},
            {"name": "boom", "description": "Always fails", "inputSchema": {"type": "object"}}]}
    elif meth == "tools/call":
        a = m["params"].get("arguments", {})
        if m["params"]["name"] == "add":
            r["result"] = {"content": [{"type": "text", "text": str(a.get("a", 0) + a.get("b", 0))}]}
        elif m["params"]["name"] == "boom":
            r["result"] = {"content": [{"type": "text", "text": "it broke"}], "isError": True}
        else:
            r["error"] = {"code": -32602, "message": "no such tool"}
    else:
        r["error"] = {"code": -32601, "message": "unknown method"}
    print(json.dumps(r), flush=True)
`

func TestMCPStdio(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found")
	}
	script := filepath.Join(t.TempDir(), "fake.py")
	_ = os.WriteFile(script, []byte(fakeMCP), 0o644)
	cl := &mcpClient{conn: ContConnector{Name: "fake", Kind: "mcp-stdio", Command: py, Args: []string{script}}, pending: map[int64]chan json.RawMessage{}}
	defer cl.close(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tools, err := cl.listTools(ctx)
	if err != nil || len(tools) != 2 || tools[0].Name != "add" || tools[0].Server != "fake" {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	if out, err := cl.callTool(ctx, "add", json.RawMessage(`{"a":2,"b":3}`)); err != nil || out != "5" {
		t.Fatalf("add: %q %v", out, err)
	}
	if _, err := cl.callTool(ctx, "boom", nil); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("a failing tool should report its error: %v", err)
	}
	if _, err := cl.callTool(ctx, "nope", nil); err == nil || !strings.Contains(err.Error(), "no such tool") {
		t.Fatalf("unknown tool: %v", err)
	}
	// The server dies: calls fail promptly with a reason instead of hanging.
	killTree(cl.cmd.Process.Pid)
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if _, err := cl.callTool(ctx, "add", nil); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("call to a dead server: %v after %s", err, time.Since(start))
	}
}

func TestMCPBadCommand(t *testing.T) {
	cl := &mcpClient{conn: ContConnector{Name: "gone", Kind: "mcp-stdio", Command: "/no/such/program"}, pending: map[int64]chan json.RawMessage{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cl.listTools(ctx); err == nil || !strings.Contains(err.Error(), "could not start gone") {
		t.Fatalf("%v", err)
	}
}
