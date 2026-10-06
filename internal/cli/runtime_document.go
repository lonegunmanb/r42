package cli

import (
	"context"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/tool/document"
)

type documentRunner interface {
	Available() bool
	Invoke(context.Context, string, document.Input) (document.Result, error)
}

func (f *runtimeFactory) documentTools(ctx context.Context, workspace string) []sdk.Tool {
	if f.documentRunner == nil || !f.documentRunner.Available() {
		return nil
	}
	return []sdk.Tool{documentReadTool(ctx, workspace, f.documentRunner.Invoke)}
}

func documentReadTool(ctx context.Context, workspace string, invoke func(context.Context, string, document.Input) (document.Result, error)) sdk.Tool {
	return sdk.Tool{
		Name: "r42_read_document",
		Description: "Convert documents in many formats to Markdown: PDF, Word (DOCX), Excel (XLSX/XLS), PowerPoint (PPTX), " +
			"HTML, CSV, JSON, XML, text, ZIP archives, and other formats supported by the installed MarkItDown and its optional dependencies. " +
			"Use this to extract readable source material from documents, especially binary PDF and Office files that plain-text readers cannot interpret. " +
			"Returns complete Markdown in content; output optionally saves it to a workspace file. " +
			"Input and output filenames must be relative to the current workspace. Preserve the converted source with the configured artifact tools before acquiring another source. " +
			"help, version, and list_plugins return CLI information instead of converting a document; cloud conversion and third-party plugins are opt-in.",
		Parameters: objectSchema(map[string]any{
			"filename":       map[string]any{"type": "string", "description": "Positional input filename relative to workspace; omit to read stdin"},
			"output":         map[string]any{"type": "string", "description": "--output / -o: output filename relative to workspace"},
			"extension":      map[string]any{"type": "string", "description": "--extension / -x: file extension hint"},
			"mime_type":      map[string]any{"type": "string", "description": "--mime-type / -m: MIME type hint"},
			"charset":        map[string]any{"type": "string", "description": "--charset / -c: input character encoding hint"},
			"use_docintel":   map[string]any{"type": "boolean", "description": "--use-docintel / -d: use Azure Document Intelligence; mutually exclusive with use_cu"},
			"endpoint":       map[string]any{"type": "string", "description": "--endpoint / -e: Document Intelligence endpoint; omission uses the CLI environment default"},
			"use_cu":         map[string]any{"type": "boolean", "description": "--use-cu / --use-content-understanding: use Azure Content Understanding"},
			"cu_endpoint":    map[string]any{"type": "string", "description": "--cu-endpoint: Content Understanding endpoint"},
			"cu_analyzer":    map[string]any{"type": "string", "description": "--cu-analyzer: Content Understanding analyzer ID"},
			"cu_file_types":  map[string]any{"type": "string", "description": "--cu-file-types: comma-separated file types to route to Content Understanding"},
			"use_plugins":    map[string]any{"type": "boolean", "description": "--use-plugins / -p: enable installed third-party plugins"},
			"list_plugins":   map[string]any{"type": "boolean", "description": "--list-plugins: list installed plugins and exit"},
			"keep_data_uris": map[string]any{"type": "boolean", "description": "--keep-data-uris: preserve embedded data URIs"},
			"version":        map[string]any{"type": "boolean", "description": "--version / -v: return the installed CLI version and exit"},
			"help":           map[string]any{"type": "boolean", "description": "--help / -h: return installed CLI help and exit"},
			"stdin":          map[string]any{"type": "string", "description": "Optional text supplied to CLI stdin when filename is omitted"},
		}, nil),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			args, err := decodeArguments[document.Input](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_arguments", err.Error())
			}
			callCtx := ctx
			if invocation.TraceContext != nil {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(invocation.TraceContext)
				defer cancel()
				stop := context.AfterFunc(ctx, cancel)
				defer stop()
				if ctx.Err() != nil {
					cancel()
				}
			}
			result, err := invoke(callCtx, workspace, args)
			if err != nil {
				return rejectedToolResult("document_conversion_failed", err.Error())
			}
			return acceptedToolResult(result)
		},
	}
}
