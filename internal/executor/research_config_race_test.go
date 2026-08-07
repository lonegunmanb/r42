package executor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/require"
)

const concurrentConfigHelper = "R42_CONCURRENT_CONFIG_HELPER"

func TestNewResearchConfigSupportsConcurrentFirstUse(t *testing.T) {
	t.Parallel()

	if os.Getenv(concurrentConfigHelper) == "1" {
		runConcurrentConfigHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestNewResearchConfigSupportsConcurrentFirstUse$")
	command.Env = append(os.Environ(), concurrentConfigHelper+"=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func runConcurrentConfigHelper(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "answer" {
  value = "42"
}
`), 0o600))

	const workers = 32
	ready := make(chan struct{})
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			<-ready
			config, err := executor.NewResearchConfig(directory, executor.ResearchConfigOptions{})
			if err == nil {
				_, err = executor.RunResearchPlan(config)
			}
			errors <- err
		})
	}
	close(ready)
	group.Wait()
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
}
