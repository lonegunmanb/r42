package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/mcp"
)

const MCPResourceReadToolName = "r42_read_mcp_resource"

type mcpResourceReader interface {
	ReadMCPResource(context.Context, mcp.ResourceReadRequest) ([]mcp.ResourceContent, error)
}

type mcpResourceReaderHolder struct {
	mu         sync.RWMutex
	authorized map[string]mcp.Resource
	reader     mcpResourceReader
}

func newMCPResourceReaderHolder(resources []mcp.Resource) *mcpResourceReaderHolder {
	authorized := make(map[string]mcp.Resource, len(resources))
	for _, resource := range resources {
		authorized[resource.ID] = resource
	}
	return &mcpResourceReaderHolder{authorized: authorized}
}

func (h *mcpResourceReaderHolder) tool() sdk.Tool {
	ids := make([]string, 0, len(h.authorized))
	mappings := make([]string, 0, len(h.authorized))
	for id, resource := range h.authorized {
		ids = append(ids, id)
		mappings = append(mappings, id+" -> "+resource.URI+" on "+resource.Server.RuntimeServerName())
	}
	slices.Sort(ids)
	slices.Sort(mappings)
	tool := mcpResourceReadTool(h, ids)
	tool.Description += " Authorized resources: " + strings.Join(mappings, "; ")
	return tool
}

func (h *mcpResourceReaderHolder) setReader(reader mcpResourceReader) {
	h.mu.Lock()
	h.reader = reader
	h.mu.Unlock()
}

func (h *mcpResourceReaderHolder) read(ctx context.Context, id string) ([]mcp.ResourceContent, error) {
	h.mu.RLock()
	resource, authorized := h.authorized[id]
	reader := h.reader
	h.mu.RUnlock()
	if !authorized {
		return nil, fmt.Errorf("resource_not_authorized: resource_id %q is not declared for this session", id)
	}
	if reader == nil {
		return nil, fmt.Errorf("mcp resource reader is unavailable")
	}
	return reader.ReadMCPResource(ctx, mcp.ResourceReadRequest{ServerName: resource.Server.RuntimeServerName(), URI: resource.URI})
}

func mcpResourceReadTool(holder *mcpResourceReaderHolder, resourceIDs []string) sdk.Tool {
	return sdk.Tool{
		Name:        MCPResourceReadToolName,
		Description: "Read an MCP resource declared for this research session. Use the declared resource_id, not an arbitrary URI.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource_id": map[string]any{
					"type":        "string",
					"description": "The declared MCP resource ID.",
					"enum":        resourceIDs,
				},
			},
			"required":             []string{"resource_id"},
			"additionalProperties": false,
		},
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, ok := invocation.Arguments.(map[string]any)
			if !ok {
				return rejectedMCPResourceToolResult("invalid_arguments", "arguments must be an object")
			}
			id, ok := arguments["resource_id"].(string)
			if !ok || id == "" {
				return rejectedMCPResourceToolResult("invalid_arguments", "resource_id must be a non-empty string")
			}
			ctx := invocation.TraceContext
			if ctx == nil {
				ctx = context.Background()
			}
			contents, err := holder.read(ctx, id)
			if err != nil {
				return rejectedMCPResourceToolResult("resource_read_failed", err.Error())
			}
			encoded, err := json.Marshal(map[string]any{"resource_id": id, "contents": contents})
			if err != nil {
				return sdk.ToolResult{}, err
			}
			return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
		},
	}
}

func rejectedMCPResourceToolResult(code, message string) (sdk.ToolResult, error) {
	encoded, err := json.Marshal(map[string]any{"accepted": false, "issues": []map[string]string{{"code": code, "message": message}}})
	if err != nil {
		return sdk.ToolResult{}, err
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}
