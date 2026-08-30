package s3_test

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientConfiguresS3CompatibleEndpointAndCredentials(t *testing.T) {
	t.Parallel()
	config := s3spec.ProviderConfig{
		Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Region: "cn-hangzhou", ForcePathStyle: true,
		AccessKeyRef: stringp("ACCESS"), SecretKeyRef: stringp("SECRET"), SessionTokenRef: stringp("TOKEN"),
	}
	values := map[string]string{"ACCESS": "access", "SECRET": "secret", "TOKEN": "token"}
	var captured *aws.Config
	client, err := internals3.NewClient(config, func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	}, func(awsConfig *aws.Config) (internals3.Client, error) {
		captured = awsConfig
		return nil, nil
	})

	require.NoError(t, err)
	assert.Nil(t, client)
	require.NotNil(t, captured)
	assert.Equal(t, config.Endpoint, aws.StringValue(captured.Endpoint))
	assert.Equal(t, config.Region, aws.StringValue(captured.Region))
	assert.True(t, aws.BoolValue(captured.S3ForcePathStyle))
	assert.Equal(t, 0, aws.IntValue(captured.MaxRetries))
	credentials, credentialErr := captured.Credentials.Get()
	require.NoError(t, credentialErr)
	assert.Equal(t, "access", credentials.AccessKeyID)
	assert.Equal(t, "secret", credentials.SecretAccessKey)
	assert.Equal(t, "token", credentials.SessionToken)
}

func TestNewClientUsesDefaultCredentialChainWhenCredentialsAreUnset(t *testing.T) {
	t.Parallel()
	var captured *aws.Config
	_, err := internals3.NewClient(s3spec.ProviderConfig{Region: "us-east-1"}, nil, func(awsConfig *aws.Config) (internals3.Client, error) {
		captured = awsConfig
		return nil, nil
	})
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Nil(t, captured.Endpoint)
	assert.Nil(t, captured.Credentials)
	assert.False(t, aws.BoolValue(captured.S3ForcePathStyle))
}

func TestNewClientReportsMissingCredentialReferenceWithoutSecret(t *testing.T) {
	t.Parallel()
	_, err := internals3.NewClient(s3spec.ProviderConfig{
		Region: "cn", AccessKeyRef: stringp("ACCESS"), SecretKeyRef: stringp("SECRET"),
	}, func(string) (string, bool) { return "", false }, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, `"ACCESS"`)
	assert.NotContains(t, err.Error(), "secret")
}

func stringp(value string) *string { return &value }
