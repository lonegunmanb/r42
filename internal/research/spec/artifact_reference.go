package spec

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

const artifactReferencePrefix = "__r42_artifact_ref_"

var artifactReferenceTokenPattern = regexp.MustCompile(
	`__r42_artifact_ref_([A-Za-z0-9_-]+)_(id|path|type|description)__`,
)

// ArtifactReferenceFunction returns references to artifacts declared by the
// current research block or dynamic task. A nil declaration map is used while
// a deferred expression is being replayed; references are then validated once
// the task has been decoded.
func ArtifactReferenceFunction(declared map[string]cty.Value) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Name: "name", Type: cty.String}},
		Type:   function.StaticReturnType(artifactValueType),
		Impl: func(arguments []cty.Value, _ cty.Type) (cty.Value, error) {
			name := strings.TrimSpace(arguments[0].AsString())
			if name == "" {
				return cty.NilVal, function.NewArgError(0, fmt.Errorf("artifact name must not be empty"))
			}
			if declared == nil {
				return artifactReferenceValue(name), nil
			}
			value, exists := declared[name]
			if !exists {
				return cty.NilVal, function.NewArgError(0, fmt.Errorf("artifact %q is not declared by this research block", name))
			}
			return value, nil
		},
	})
}

func artifactReferenceValue(name string) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"id":          cty.StringVal(artifactReferenceToken(name, "id")),
		"name":        cty.StringVal(name),
		"kind":        cty.StringVal("artifact"),
		"type":        cty.StringVal(artifactReferenceToken(name, "type")),
		"path":        cty.StringVal(artifactReferenceToken(name, "path")),
		"description": cty.StringVal(artifactReferenceToken(name, "description")),
		"required":    cty.False,
		"non_empty":   cty.False,
	})
}

func artifactReferenceToken(name, field string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(name))
	return artifactReferencePrefix + encoded + "_" + field + "__"
}

func parseArtifactReferenceToken(value string) (string, string, bool) {
	match := artifactReferenceTokenPattern.FindStringSubmatch(value)
	if len(match) != 3 || match[0] != value {
		return "", "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(match[1])
	if err != nil {
		return "", "", false
	}
	return string(decoded), match[2], true
}

// ArtifactReferenceIDName returns the declared artifact name represented by a
// deferred artifact ID reference.
func ArtifactReferenceIDName(value string) (string, bool) {
	name, field, ok := parseArtifactReferenceToken(value)
	return name, ok && field == "id"
}

// ResolveArtifactReferences resolves metadata references after artifact
// declarations are available. IDs remain deferred until the registry assigns
// them during apply.
func ResolveArtifactReferences(config Config) (Config, error) {
	artifacts := make(map[string]Artifact, len(config.Artifacts))
	for _, artifact := range config.Artifacts {
		artifacts[artifact.Name] = artifact
	}
	var err error
	if config.SystemPrompt, err = resolveArtifactReferenceString(config.SystemPrompt, artifacts); err != nil {
		return Config{}, err
	}
	if config.Prompt != nil {
		prompt, resolveErr := resolveArtifactReferenceString(*config.Prompt, artifacts)
		if resolveErr != nil {
			return Config{}, resolveErr
		}
		config.Prompt = &prompt
	}
	config.ToolUses = slices.Clone(config.ToolUses)
	for index := range config.ToolUses {
		input, resolveErr := resolveArtifactReferenceValue(config.ToolUses[index].Input, artifacts)
		if resolveErr != nil {
			return Config{}, fmt.Errorf("tool_use %q input: %w", config.ToolUses[index].Name, resolveErr)
		}
		config.ToolUses[index].Input = input
		agent, resolveErr := resolveArtifactReferenceValue(config.ToolUses[index].InputFromAgent, artifacts)
		if resolveErr != nil {
			return Config{}, fmt.Errorf("tool_use %q input_from_agent: %w", config.ToolUses[index].Name, resolveErr)
		}
		config.ToolUses[index].InputFromAgent = agent
	}
	return config, nil
}

func resolveArtifactReferenceString(value string, artifacts map[string]Artifact) (string, error) {
	for _, match := range artifactReferenceTokenPattern.FindAllString(value, -1) {
		name, field, ok := parseArtifactReferenceToken(match)
		if !ok {
			continue
		}
		artifact, exists := artifacts[name]
		if !exists {
			return "", fmt.Errorf("artifact(%q) references an undeclared artifact", name)
		}
		switch field {
		case "id":
			continue
		case "path":
			value = strings.ReplaceAll(value, match, artifact.Path)
		case "type":
			value = strings.ReplaceAll(value, match, string(artifact.Type))
		case "description":
			value = strings.ReplaceAll(value, match, artifact.Description)
		}
	}
	return value, nil
}

func resolveArtifactReferenceValue(value cty.Value, artifacts map[string]Artifact) (cty.Value, error) {
	if value == cty.NilVal {
		return value, nil
	}
	unmarked, marks := value.Unmark()
	if !unmarked.IsKnown() || unmarked.IsNull() {
		return value, nil
	}
	switch {
	case unmarked.Type().Equals(cty.String):
		resolved, err := resolveArtifactReferenceString(unmarked.AsString(), artifacts)
		if err != nil {
			return cty.NilVal, err
		}
		return cty.StringVal(resolved).WithMarks(marks), nil
	case unmarked.Type().IsObjectType():
		resolved, ok, err := resolveArtifactReferenceObject(unmarked, artifacts)
		if err != nil {
			return cty.NilVal, err
		}
		if ok {
			return resolved.WithMarks(marks), nil
		}
		values := make(map[string]cty.Value, len(unmarked.AsValueMap()))
		for name, item := range unmarked.AsValueMap() {
			resolved, err := resolveArtifactReferenceValue(item, artifacts)
			if err != nil {
				return cty.NilVal, err
			}
			values[name] = resolved
		}
		return cty.ObjectVal(values).WithMarks(marks), nil
	case unmarked.Type().IsMapType():
		values := make(map[string]cty.Value, len(unmarked.AsValueMap()))
		for name, item := range unmarked.AsValueMap() {
			resolved, err := resolveArtifactReferenceValue(item, artifacts)
			if err != nil {
				return cty.NilVal, err
			}
			values[name] = resolved
		}
		if len(values) == 0 {
			return cty.MapValEmpty(unmarked.Type().ElementType()).WithMarks(marks), nil
		}
		return cty.MapVal(values).WithMarks(marks), nil
	case unmarked.Type().IsListType():
		items := unmarked.AsValueSlice()
		for index := range items {
			resolved, err := resolveArtifactReferenceValue(items[index], artifacts)
			if err != nil {
				return cty.NilVal, err
			}
			items[index] = resolved
		}
		if len(items) == 0 {
			return cty.ListValEmpty(unmarked.Type().ElementType()).WithMarks(marks), nil
		}
		return cty.ListVal(items).WithMarks(marks), nil
	case unmarked.Type().IsTupleType():
		items := unmarked.AsValueSlice()
		for index := range items {
			resolved, err := resolveArtifactReferenceValue(items[index], artifacts)
			if err != nil {
				return cty.NilVal, err
			}
			items[index] = resolved
		}
		return cty.TupleVal(items).WithMarks(marks), nil
	default:
		return value, nil
	}
}

func resolveArtifactReferenceObject(
	value cty.Value,
	artifacts map[string]Artifact,
) (cty.Value, bool, error) {
	if !value.Type().Equals(artifactValueType) {
		return cty.NilVal, false, nil
	}
	id := value.GetAttr("id")
	if !id.IsKnown() || id.IsNull() || !id.Type().Equals(cty.String) {
		return cty.NilVal, false, nil
	}
	name, ok := ArtifactReferenceIDName(id.AsString())
	if !ok {
		return cty.NilVal, false, nil
	}
	artifact, exists := artifacts[name]
	if !exists {
		return cty.NilVal, true, fmt.Errorf("artifact(%q) references an undeclared artifact", name)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"id":          id,
		"name":        cty.StringVal(artifact.Name),
		"kind":        cty.StringVal("artifact"),
		"type":        cty.StringVal(string(artifact.Type)),
		"path":        cty.StringVal(artifact.Path),
		"description": cty.StringVal(artifact.Description),
		"required":    cty.BoolVal(artifact.Required),
		"non_empty":   cty.BoolVal(artifact.NonEmpty),
	}), true, nil
}
