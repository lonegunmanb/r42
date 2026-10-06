package cli_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/tool/document"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type startupDocumentRunner struct{ installed bool }

func (r startupDocumentRunner) Available() bool { return r.installed }
func (startupDocumentRunner) Invoke(context.Context, string, document.Input) (document.Result, error) {
	return document.Result{Content: "# Document"}, nil
}

func TestProductionRuntimeRegistersStartupDocumentsInCollection(t *testing.T) {
	t.Parallel()
	for _, installed := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("installed=%t/module=%t", installed, nested), func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				if nested {
					root = writeModuleFixture(t, "")
				} else {
					require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42.hcl"), []byte(`
research "static" "documents" {
  model = "test-model"
  system_prompt = "Read documents."
}
`), 0o600))
				}
				opener := &fakeSessionOpener{}
				runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener, DocumentRunner: startupDocumentRunner{installed: installed}})
				planned, err := planRuntime(runtime, t.Context(), root, nil)
				require.NoError(t, err)
				_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})
				require.NoError(t, err)
				require.Len(t, opener.configs, 3)
				assert.Equal(t, installed, slices.Contains(toolNamesFromConfig(opener.configs[0]), "r42_read_document"))
				assert.NotContains(t, toolNamesFromConfig(opener.configs[1]), "r42_read_document")
				assert.NotContains(t, toolNamesFromConfig(opener.configs[2]), "r42_read_document")
			})
		}
	}
}
