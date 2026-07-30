package spec

import (
	"fmt"
	"strings"
)

type ArtifactType string

const (
	ArtifactTypeFile      ArtifactType = "file"
	ArtifactTypeDirectory ArtifactType = "directory"
)

type Artifact struct {
	Name     string
	Type     ArtifactType
	Path     string
	Required bool
	NonEmpty bool
}

func (a Artifact) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("artifact name is required")
	}
	if a.Type != ArtifactTypeFile && a.Type != ArtifactTypeDirectory {
		return fmt.Errorf("artifact %s type must be file or directory", a.Name)
	}
	if strings.TrimSpace(a.Path) == "" {
		return fmt.Errorf("artifact %s path is required", a.Name)
	}
	return nil
}
