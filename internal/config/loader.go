package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

type SourceFile struct {
	Path   string
	Source []byte
}

type Loaded struct {
	Files  []SourceFile
	Blocks []*golden.HclBlock
}

func LoadDirectory(directory string) (Loaded, hcl.Diagnostics, error) {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		// note: untested because safely making the process working directory unavailable is platform-dependent.
		return Loaded{}, nil, fmt.Errorf("resolving config directory: %w", err)
	}
	entries, err := os.ReadDir(absoluteDirectory)
	if err != nil {
		return Loaded{}, nil, fmt.Errorf("reading config directory %q: %w", absoluteDirectory, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	loaded := Loaded{
		Files:  []SourceFile{},
		Blocks: []*golden.HclBlock{},
	}
	var diagnostics hcl.Diagnostics
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".r42" {
			continue
		}

		path := filepath.Join(absoluteDirectory, entry.Name())
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			// note: untested because reproducing a file disappearing between ReadDir and ReadFile is platform-dependent.
			return Loaded{}, diagnostics, fmt.Errorf("reading config file %q: %w", path, readErr)
		}
		loaded.Files = append(loaded.Files, SourceFile{Path: path, Source: source})

		syntaxFile, syntaxDiagnostics := hclsyntax.ParseConfig(source, path, hcl.InitialPos)
		writeFile, writeDiagnostics := hclwrite.ParseConfig(source, path, hcl.InitialPos)
		diagnostics = append(diagnostics, syntaxDiagnostics...)
		diagnostics = append(diagnostics, writeDiagnostics...)
		if syntaxDiagnostics.HasErrors() || writeDiagnostics.HasErrors() {
			continue
		}

		body, ok := syntaxFile.Body.(*hclsyntax.Body)
		if !ok {
			// note: untested because hclsyntax.ParseConfig always returns an hclsyntax.Body after a successful parse.
			return Loaded{}, diagnostics, fmt.Errorf("config file %q has an unexpected syntax body", path)
		}
		loaded.Blocks = append(loaded.Blocks, golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks())...)
	}

	return loaded, diagnostics, nil
}
