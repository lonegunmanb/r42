package spec_test

import (
	"testing"

	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestS3ProviderConfigValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  s3spec.ProviderConfig
		want string
	}{
		{name: "region required", cfg: s3spec.ProviderConfig{}, want: "region is required"},
		{name: "endpoint must be https", cfg: s3spec.ProviderConfig{Region: "cn", Endpoint: "http://oss.example.test"}, want: "HTTPS URL"},
		{name: "credential pair", cfg: s3spec.ProviderConfig{Region: "cn", AccessKey: str("a"), AccessKeyRef: str("A")}, want: "access_key and access_key_ref"},
		{name: "secret pair", cfg: s3spec.ProviderConfig{Region: "cn", SecretKey: str("s"), SecretKeyRef: str("S")}, want: "secret_key and secret_key_ref"},
		{name: "token pair", cfg: s3spec.ProviderConfig{Region: "cn", SessionToken: str("t"), SessionTokenRef: str("T")}, want: "session_token and session_token_ref"},
		{name: "access and secret required together", cfg: s3spec.ProviderConfig{Region: "cn", AccessKey: str("a")}, want: "access_key and secret_key"},
		{name: "retry bounds", cfg: s3spec.ProviderConfig{Region: "cn", Retry: s3spec.RetryOverride{MaxRetries: intp(-1)}}, want: "max retries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestS3FolderConfigValidate(t *testing.T) {
	t.Parallel()
	valid := s3spec.FolderConfig{Provider: providerReference(), Bucket: "bucket", Source: "run/output", Prefix: "a/b"}
	tests := []struct {
		name string
		cfg  s3spec.FolderConfig
		want string
	}{
		{name: "bucket required", cfg: s3spec.FolderConfig{Source: "source"}, want: "bucket is required"},
		{name: "provider required", cfg: s3spec.FolderConfig{Bucket: "bucket", Source: "source"}, want: "provider is required"},
		{name: "provider kind", cfg: s3spec.FolderConfig{Provider: cty.ObjectVal(map[string]cty.Value{"address": cty.StringVal("model_provider.main"), "kind": cty.StringVal("provider")}), Bucket: "bucket", Source: "source"}, want: "s3_provider reference"},
		{name: "source required", cfg: s3spec.FolderConfig{Provider: providerReference(), Bucket: "bucket"}, want: "source is required"},
		{name: "prefix trailing slash", cfg: s3spec.FolderConfig{Provider: providerReference(), Bucket: "bucket", Source: "source", Prefix: "a/"}, want: "prefix must not end"},
		{name: "prefix traversal", cfg: s3spec.FolderConfig{Provider: providerReference(), Bucket: "bucket", Source: "source", Prefix: "a/../b"}, want: "prefix must not contain"},
		{name: "prefix backslash", cfg: s3spec.FolderConfig{Provider: providerReference(), Bucket: "bucket", Source: "source", Prefix: `a\\b`}, want: "prefix must not contain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
	require.NoError(t, valid.Validate())
}

func TestS3ProviderValueDoesNotExposeCredentialValues(t *testing.T) {
	t.Parallel()
	block := s3spec.ProviderBlockValue("s3_provider.oss", s3spec.ProviderConfig{
		Region: "cn-hangzhou", AccessKey: str("secret-access"), SecretKey: str("secret-key"),
	})
	assert.Equal(t, "s3_provider.oss", block.GetAttr("address").AsString())
	assert.NotContains(t, block.GoString(), "secret-access")
	assert.NotContains(t, block.GoString(), "secret-key")
	assert.True(t, block.GetAttr("access_key").IsMarked())
	assert.True(t, block.GetAttr("secret_key").IsMarked())
}

func TestS3FolderValueShape(t *testing.T) {
	t.Parallel()
	value := s3spec.FolderBlockValue("s3_folder.out", s3spec.FolderConfig{Bucket: "b", Source: "s", Prefix: "p"})
	assert.Equal(t, "s3_folder.out", value.GetAttr("address").AsString())
	assert.Equal(t, "b", value.GetAttr("bucket").AsString())
	assert.Equal(t, "s", value.GetAttr("source").AsString())
	assert.Equal(t, "p", value.GetAttr("prefix").AsString())
	assert.True(t, value.GetAttr("result").Type().IsObjectType())
	assert.True(t, value.GetAttr("result").Type().HasAttribute("object_count"))
}

func str(value string) *string { return &value }
func intp(value int) *int      { return &value }

func providerReference() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal("s3_provider.oss"),
		"kind":    cty.StringVal("s3_provider"),
	})
}
