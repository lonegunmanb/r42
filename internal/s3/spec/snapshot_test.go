package spec_test

import (
	"testing"

	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestFolderPlanSnapshotRoundTripPreservesProviderAndFolderConfig(t *testing.T) {
	t.Parallel()
	provider := s3spec.ProviderConfig{Endpoint: "https://oss.example.test", Region: "cn", AccessKeyRef: str("ACCESS"), SecretKeyRef: str("SECRET"), ForcePathStyle: true}
	folder := s3spec.FolderConfig{Bucket: "bucket", Source: "blocks/result", Prefix: "reports/day", Exclude: []string{"**/*.tmp"}}
	encoded, err := s3spec.EncodeFolderPlan(provider, folder)
	require.NoError(t, err)
	decodedProvider, decodedFolder, err := s3spec.DecodeFolderPlan(encoded)
	require.NoError(t, err)
	assert.Equal(t, provider, decodedProvider)
	assert.Equal(t, folder.Bucket, decodedFolder.Bucket)
	assert.Equal(t, folder.Source, decodedFolder.Source)
	assert.Equal(t, folder.Prefix, decodedFolder.Prefix)
	assert.Equal(t, folder.Exclude, decodedFolder.Exclude)
}

func TestFolderPlanRunIDSubfolderRoundTrip(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			folder := s3spec.FolderConfig{Bucket: "bucket", Source: "source", UseRunIDSubfolder: enabled}
			encoded, err := s3spec.EncodeFolderPlan(s3spec.ProviderConfig{Region: "region"}, folder)
			require.NoError(t, err)
			_, decoded, err := s3spec.DecodeFolderPlan(encoded)
			require.NoError(t, err)
			assert.Equal(t, enabled, decoded.UseRunIDSubfolder)
		})
	}
}

func TestFolderPlanLegacySnapshotDefaultsToNoRunIDSubfolder(t *testing.T) {
	t.Parallel()
	legacy := cty.ObjectVal(map[string]cty.Value{
		"payload": cty.StringVal(`{"provider":{"Region":"region"},"folder":{"bucket":"bucket","source":"source","prefix":"reports"}}`),
	})
	_, folder, err := s3spec.DecodeFolderPlan(legacy)
	require.NoError(t, err)
	assert.False(t, folder.UseRunIDSubfolder)
	assert.Equal(t, "reports", folder.Prefix)
}

func TestFolderPlanSnapshotMarksLiteralCredentialsSensitive(t *testing.T) {
	t.Parallel()
	encoded, err := s3spec.EncodeFolderPlan(s3spec.ProviderConfig{Region: "region", AccessKey: str("access"), SecretKey: str("secret")}, s3spec.FolderConfig{Bucket: "bucket", Source: "source"})
	require.NoError(t, err)
	assert.True(t, encoded.GetAttr("payload").IsMarked())
}
