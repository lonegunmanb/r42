package s3_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRollbackUploadedDeletesExactVersionsInReverseOrder(t *testing.T) {
	t.Parallel()
	client := &fakeClient{delete: func(*awss3.DeleteObjectInput) (*awss3.DeleteObjectOutput, error) {
		return &awss3.DeleteObjectOutput{}, nil
	}}
	available, err := internals3.RollbackUploaded(t.Context(), client, "bucket", internals3.VersioningEnabled, []internals3.UploadedObject{{Key: "first", VersionID: "v1"}, {Key: "second", VersionID: "v2"}}, internals3.RetryPolicy{})
	require.NoError(t, err)
	assert.True(t, available)
	assert.Equal(t, []string{"second@v2", "first@v1"}, client.deleted)
}

func TestBucketVersioningStatusTreatsOmittedStatusAsDisabled(t *testing.T) {
	t.Parallel()
	client := &fakeClient{versioning: func(*awss3.GetBucketVersioningInput) (*awss3.GetBucketVersioningOutput, error) {
		return &awss3.GetBucketVersioningOutput{}, nil
	}}
	status, err := internals3.BucketVersioningStatus(t.Context(), client, "bucket", internals3.RetryPolicy{})
	require.NoError(t, err)
	assert.Equal(t, internals3.VersioningDisabled, status)
}

func TestRollbackUploadedSkipsUnversionedAndSuspendedBuckets(t *testing.T) {
	t.Parallel()
	for _, status := range []string{internals3.VersioningDisabled, internals3.VersioningSuspended, ""} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{delete: func(*awss3.DeleteObjectInput) (*awss3.DeleteObjectOutput, error) {
				t.Fatal("delete must not run")
				return nil, nil
			}}
			available, err := internals3.RollbackUploaded(t.Context(), client, "bucket", status, []internals3.UploadedObject{{Key: "key", VersionID: "version"}}, internals3.RetryPolicy{})
			require.NoError(t, err)
			assert.False(t, available)
		})
	}
}

func TestRollbackUploadedAggregatesCleanupErrors(t *testing.T) {
	t.Parallel()
	client := &fakeClient{delete: func(input *awss3.DeleteObjectInput) (*awss3.DeleteObjectOutput, error) {
		return nil, errors.New("delete " + aws.StringValue(input.Key))
	}}
	_, err := internals3.RollbackUploaded(t.Context(), client, "bucket", internals3.VersioningEnabled, []internals3.UploadedObject{{Key: "first", VersionID: "v1"}, {Key: "second", VersionID: "v2"}}, internals3.RetryPolicy{})
	require.Error(t, err)
	require.ErrorContains(t, err, "delete first")
	require.ErrorContains(t, err, "delete second")
	assert.Equal(t, []string{"second@v2", "first@v1"}, client.deleted)
}

func TestUploadFailureReportsLocalAndRemoteRoots(t *testing.T) {
	t.Parallel()
	err := internals3.UploadFailure(`C:\run\blocks\result`, "bucket", "reports/day", "reports/day/file.txt", errors.New("put failed"), errors.New("rollback failed"))
	require.Error(t, err)
	require.ErrorContains(t, err, `C:\run\blocks\result`)
	require.ErrorContains(t, err, "s3://bucket/reports/day")
	require.ErrorContains(t, err, "reports/day/file.txt")
	require.ErrorContains(t, err, "put failed")
	assert.ErrorContains(t, err, "rollback failed")
}
