package evidence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSourceTableReplacesModelAuthoredSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge.json")
	report := filepath.Join(root, "report.md")
	require.NoError(t, os.WriteFile(knowledge, []byte(`{"quotes":[{"id":"topic-quote-001","url":"https://canonical.example/source"}]}`), 0o600))
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nClaim [topic-quote-001]\n\n## Sources\n\n| Quote ID | URL |\n| --- | --- |\n| topic-quote-001 | https://wrong.example |\n"), 0o600))

	rows, err := GenerateSourceTable(report, []string{knowledge})
	require.NoError(t, err)
	assert.Equal(t, 1, rows)
	content, err := os.ReadFile(report)
	require.NoError(t, err)
	assert.Contains(t, string(content), "https://canonical.example/source")
	assert.NotContains(t, string(content), "https://wrong.example")
}

func TestGenerateSourceTableRemovesDerivedQuotesAndLinksCanonicalSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge.json")
	report := filepath.Join(root, "report.md")
	require.NoError(t, os.WriteFile(knowledge, []byte(`{"quotes":[
{"id":"derived-quote-001","url":"model-derived calculation snapshot based on upstream evidence"},
{"id":"topic-quote-002","url":"https://canonical.example/source"}
]}`), 0o600))
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nDerived [derived-quote-001](https://wrong.example/derived) and sourced [topic-quote-002](https://wrong.example/topic-quote-999).\n\n## Sources\n\n| Quote ID | URL |\n| --- | --- |\n| derived-quote-001 | model-derived calculation snapshot based on upstream evidence |\n| topic-quote-002 | https://wrong.example |\n"), 0o600))

	rows, err := GenerateSourceTable(report, []string{knowledge})
	require.NoError(t, err)
	assert.Equal(t, 1, rows)
	content, err := os.ReadFile(report)
	require.NoError(t, err)
	text := string(content)
	assert.NotContains(t, text, "derived-quote-001")
	assert.Contains(t, text, "[topic-quote-002](https://canonical.example/source)")
	assert.Contains(t, text, "| topic-quote-002 | https://canonical.example/source |")
	assert.NotContains(t, text, "https://wrong.example")
}

func TestGenerateSourceTableAcceptsUppercaseHTTPScheme(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge.json")
	report := filepath.Join(root, "report.md")
	require.NoError(t, os.WriteFile(knowledge, []byte(`{"quotes":[{"id":"topic-quote-003","url":"HTTPS://canonical.example/source"}]}`), 0o600))
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nClaim [topic-quote-003]\n"), 0o600))

	rows, err := GenerateSourceTable(report, []string{knowledge})
	require.NoError(t, err)
	assert.Equal(t, 1, rows)
	content, err := os.ReadFile(report)
	require.NoError(t, err)
	assert.Contains(t, string(content), "[topic-quote-003](HTTPS://canonical.example/source)")
}

func TestGenerateSourceTableRepairsBareQuoteReference(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge.json")
	report := filepath.Join(root, "report.md")
	require.NoError(t, os.WriteFile(knowledge, []byte(`{"quotes":[{"id":"quote-6c93f7b3a59f82db","url":"https://canonical.example/source"}]}`), 0o600))
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nClaim [quote-6c93f7b3a59f82db]\n"), 0o600))

	rows, err := GenerateSourceTable(report, []string{knowledge})
	require.NoError(t, err)
	assert.Equal(t, 1, rows)
	content, err := os.ReadFile(report)
	require.NoError(t, err)
	text := string(content)
	assert.Contains(t, text, "[quote-6c93f7b3a59f82db](https://canonical.example/source)")
	assert.Contains(t, text, "| quote-6c93f7b3a59f82db | https://canonical.example/source |")
}

func TestGenerateSourceTableRemovesUnrepairableQuoteReference(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	knowledge := filepath.Join(root, "knowledge.json")
	report := filepath.Join(root, "report.md")
	require.NoError(t, os.WriteFile(knowledge, []byte(`{"quotes":[{"id":"quote-derived-001","url":"derived calculation snapshot"}]}`), 0o600))
	require.NoError(t, os.WriteFile(report, []byte("# Report\n\nKeep [ordinary-label] and remove [quote-derived-001].\n"), 0o600))

	rows, err := GenerateSourceTable(report, []string{knowledge})
	require.NoError(t, err)
	assert.Equal(t, 0, rows)
	content, err := os.ReadFile(report)
	require.NoError(t, err)
	text := string(content)
	assert.Contains(t, text, "[ordinary-label]")
	assert.NotContains(t, text, "quote-derived-001")
}
