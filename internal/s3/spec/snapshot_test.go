package spec_test

import (
	"testing"

	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestFolderPlanSnapshotMarksLiteralCredentialsSensitive(t *testing.T) {
	t.Parallel()
	encoded, err := s3spec.EncodeFolderPlan(s3spec.ProviderConfig{Region: "region", AccessKey: str("access"), SecretKey: str("secret")}, s3spec.FolderConfig{Bucket: "bucket", Source: "source"})
	require.NoError(t, err)
	assert.True(t, encoded.GetAttr("payload").IsMarked())
}
