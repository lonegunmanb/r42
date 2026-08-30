package spec

import (
	"encoding/json"
	"fmt"

	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type folderPlanSnapshot struct {
	Provider ProviderConfig       `json:"provider"`
	Folder   folderConfigSnapshot `json:"folder"`
}

type folderConfigSnapshot struct {
	Bucket  string        `json:"bucket"`
	Source  string        `json:"source"`
	Prefix  string        `json:"prefix"`
	Exclude []string      `json:"exclude,omitempty"`
	Retry   RetryOverride `json:"retry"`
}

func EncodeFolderPlan(provider ProviderConfig, folder FolderConfig) (cty.Value, error) {
	payload, err := json.Marshal(folderPlanSnapshot{
		Provider: provider,
		Folder:   folderConfigSnapshot{Bucket: folder.Bucket, Source: folder.Source, Prefix: folder.Prefix, Exclude: append([]string(nil), folder.Exclude...), Retry: folder.Retry},
	})
	if err != nil {
		return cty.NilVal, fmt.Errorf("encode S3 folder plan: %w", err)
	}
	value := cty.StringVal(string(payload))
	if provider.AccessKey != nil || provider.SecretKey != nil || provider.SessionToken != nil {
		value = corespec.MarkSensitive(value)
	}
	return cty.ObjectVal(map[string]cty.Value{"payload": value}), nil
}

func DecodeFolderPlan(value cty.Value) (ProviderConfig, FolderConfig, error) {
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || unmarked.IsNull() || !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("payload") {
		return ProviderConfig{}, FolderConfig{}, fmt.Errorf("S3 folder plan snapshot is invalid")
	}
	payload := unmarked.GetAttr("payload")
	if payload.IsNull() || !payload.IsKnown() || !payload.Type().Equals(cty.String) {
		return ProviderConfig{}, FolderConfig{}, fmt.Errorf("S3 folder plan payload is invalid")
	}
	var snapshot folderPlanSnapshot
	if err := json.Unmarshal([]byte(payload.AsString()), &snapshot); err != nil {
		return ProviderConfig{}, FolderConfig{}, fmt.Errorf("decode S3 folder plan: %w", err)
	}
	folder := FolderConfig{Bucket: snapshot.Folder.Bucket, Source: snapshot.Folder.Source, Prefix: snapshot.Folder.Prefix, Exclude: snapshot.Folder.Exclude, Retry: snapshot.Folder.Retry}
	if err := snapshot.Provider.Validate(); err != nil {
		return ProviderConfig{}, FolderConfig{}, fmt.Errorf("S3 provider snapshot: %w", err)
	}
	if folder.Bucket == "" || folder.Source == "" {
		return ProviderConfig{}, FolderConfig{}, fmt.Errorf("S3 folder snapshot is invalid")
	}
	return snapshot.Provider, folder, nil
}
