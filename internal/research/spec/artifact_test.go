package spec_test

import (
	"testing"

	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
)

func TestArtifactValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		artifact      researchspec.Artifact
		expectedError string
	}{
		{name: "file", artifact: researchspec.Artifact{Name: "report", Type: researchspec.ArtifactTypeFile, Path: "report.md", Required: true, NonEmpty: true}},
		{name: "directory with escaping path", artifact: researchspec.Artifact{Name: "dataset", Type: researchspec.ArtifactTypeDirectory, Path: "../shared"}},
		{name: "absolute path", artifact: researchspec.Artifact{Name: "dataset", Type: researchspec.ArtifactTypeDirectory, Path: `D:\shared`}},
		{name: "missing name", artifact: researchspec.Artifact{Type: researchspec.ArtifactTypeFile, Path: "report.md"}, expectedError: "artifact name is required"},
		{name: "invalid type", artifact: researchspec.Artifact{Name: "report", Type: "archive", Path: "report.zip"}, expectedError: "artifact report type must be file or directory"},
		{name: "missing path", artifact: researchspec.Artifact{Name: "report", Type: researchspec.ArtifactTypeFile}, expectedError: "artifact report path is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expectedError, errorString(tt.artifact.Validate()))
		})
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
