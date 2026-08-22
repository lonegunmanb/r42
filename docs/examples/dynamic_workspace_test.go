package examples_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDynamicResearchPathsUseRuntimeTaskIndex(t *testing.T) {
	t.Parallel()

	files := exampleHCLFiles(t)
	blockPath := regexp.MustCompile(`\$\{block_wd\(\)\}/\$\{([^}]+)\}`)
	formattedPath := regexp.MustCompile(`block_wd\(\),\s*([^,)]+)`)

	for _, filename := range files {
		t.Run(filepath.ToSlash(filename), func(t *testing.T) {
			t.Parallel()

			payload, err := os.ReadFile(filename)
			require.NoError(t, err)
			parser := hclparse.NewParser()
			file, diagnostics := parser.ParseHCL(payload, filename)
			require.False(t, diagnostics.HasErrors(), diagnostics.Error())
			body, ok := file.Body.(*hclsyntax.Body)
			require.True(t, ok)

			for _, block := range body.Blocks {
				if block.Type != "research" || len(block.Labels) != 2 || block.Labels[0] != "dynamic" {
					continue
				}
				source := string(block.Range().SliceBytes(payload))
				for _, match := range blockPath.FindAllStringSubmatch(source, -1) {
					assert.Equal(t, "index", match[1], "%s must use the runtime's zero-based task index", block.Labels[1])
				}
				for _, match := range formattedPath.FindAllStringSubmatch(source, -1) {
					assert.Equal(t, "index", match[1], "%s must use the runtime's zero-based task index", block.Labels[1])
				}
			}
		})
	}
}

func exampleHCLFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".hcl" {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err)
	return files
}
