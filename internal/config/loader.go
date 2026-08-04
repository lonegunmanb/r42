package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
)

type SourceFile struct {
	Path   string
	Source []byte
}

type Loaded struct {
	Files  []SourceFile
	Blocks []*golden.HclBlock
}

func LoadDirectoryContext(ctx context.Context, directory string) (Loaded, hcl.Diagnostics, error) {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		// note: untested because safely making the process working directory unavailable is platform-dependent.
		return Loaded{}, nil, fmt.Errorf("resolving config directory: %w", err)
	}
	scanStarted := time.Now()
	if err = debuglog.Lifecycle(ctx, "config.directory.scan", debuglog.StatusStarted, debuglog.Event{
		Path: absoluteDirectory,
	}); err != nil {
		return Loaded{}, nil, err
	}
	entries, err := os.ReadDir(absoluteDirectory)
	if err != nil {
		logErr := debuglog.CompleteLifecycle(ctx, "config.directory.scan", scanStarted, err, debuglog.Event{
			Path: absoluteDirectory,
		})
		return Loaded{}, nil, fmt.Errorf("reading config directory %q: %w", absoluteDirectory, errors.Join(err, logErr))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	loaded := Loaded{
		Files:  []SourceFile{},
		Blocks: []*golden.HclBlock{},
	}
	var diagnostics hcl.Diagnostics
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".r42.hcl") {
			continue
		}

		path := filepath.Join(absoluteDirectory, entry.Name())
		collectStarted := time.Now()
		if err = debuglog.Lifecycle(ctx, "config.file.collect", debuglog.StatusStarted, debuglog.Event{Path: path}); err != nil {
			return Loaded{}, diagnostics, err
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			logErr := debuglog.CompleteLifecycle(ctx, "config.file.collect", collectStarted, readErr, debuglog.Event{Path: path})
			// note: untested because reproducing a file disappearing between ReadDir and ReadFile is platform-dependent.
			return Loaded{}, diagnostics, fmt.Errorf("reading config file %q: %w", path, errors.Join(readErr, logErr))
		}
		if err = debuglog.CompleteLifecycle(ctx, "config.file.collect", collectStarted, nil, debuglog.Event{
			Path: path, Bytes: len(source),
		}); err != nil {
			return Loaded{}, diagnostics, err
		}
		loaded.Files = append(loaded.Files, SourceFile{Path: path, Source: source})

		syntaxStarted := time.Now()
		if err = debuglog.Lifecycle(ctx, "hcl.syntax.parse", debuglog.StatusStarted, debuglog.Event{Path: path, Bytes: len(source)}); err != nil {
			return Loaded{}, diagnostics, err
		}
		syntaxFile, syntaxDiagnostics := hclsyntax.ParseConfig(source, path, hcl.InitialPos)
		syntaxErr := errors.Join(syntaxDiagnostics.Errs()...)
		if err = debuglog.CompleteLifecycle(ctx, "hcl.syntax.parse", syntaxStarted, syntaxErr, debuglog.Event{Path: path}); err != nil {
			return Loaded{}, diagnostics, err
		}
		writeStarted := time.Now()
		if err = debuglog.Lifecycle(ctx, "hcl.write.parse", debuglog.StatusStarted, debuglog.Event{Path: path, Bytes: len(source)}); err != nil {
			return Loaded{}, diagnostics, err
		}
		writeFile, writeDiagnostics := hclwrite.ParseConfig(source, path, hcl.InitialPos)
		writeErr := errors.Join(writeDiagnostics.Errs()...)
		if err = debuglog.CompleteLifecycle(ctx, "hcl.write.parse", writeStarted, writeErr, debuglog.Event{Path: path}); err != nil {
			return Loaded{}, diagnostics, err
		}
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
		blocks := golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks())
		for _, block := range blocks {
			if err = debuglog.Lifecycle(ctx, "hcl.block.extract", debuglog.StatusCompleted, debuglog.Event{
				Path: path, BlockType: block.Type, BlockAddress: sourceBlockAddress(block),
				SourceRange: block.Range().String(),
			}); err != nil {
				return Loaded{}, diagnostics, err
			}
		}
		loaded.Blocks = append(loaded.Blocks, blocks...)
	}
	if err = debuglog.CompleteLifecycle(ctx, "config.directory.scan", scanStarted, nil, debuglog.Event{
		Path: absoluteDirectory, Paths: sourcePaths(loaded.Files), Count: len(loaded.Blocks),
	}); err != nil {
		return Loaded{}, diagnostics, err
	}

	return loaded, diagnostics, nil
}

func sourceBlockAddress(block *golden.HclBlock) string {
	parts := []string{block.Type}
	for _, label := range block.Labels {
		if strings.TrimSpace(label) != "" {
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, ".")
}

func sourcePaths(files []SourceFile) []string {
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.Path
	}
	return paths
}
