package s3

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

const (
	VersioningEnabled   = awss3.BucketVersioningStatusEnabled
	VersioningDisabled  = "Disabled"
	VersioningSuspended = awss3.BucketVersioningStatusSuspended
)

func BucketVersioningStatus(ctx context.Context, client Client, bucket string, retry RetryPolicy) (string, error) {
	status := VersioningDisabled
	err := Retry(ctx, retry, func(ctx context.Context) error {
		result, err := client.GetBucketVersioningWithContext(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		if err != nil {
			return err
		}
		if result != nil && aws.StringValue(result.Status) != "" {
			status = aws.StringValue(result.Status)
		}
		return nil
	})
	return status, err
}

func RollbackUploaded(ctx context.Context, client Client, bucket, versioning string, uploaded []UploadedObject, retry RetryPolicy) (bool, error) {
	if versioning != VersioningEnabled {
		return false, nil
	}
	var cleanup error
	for _, object := range slices.Backward(uploaded) {
		if object.VersionID == "" {
			cleanup = errors.Join(cleanup, fmt.Errorf("uploaded object %q has no version ID", object.Key))
			continue
		}
		err := Retry(ctx, retry, func(ctx context.Context) error {
			_, err := client.DeleteObjectWithContext(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(object.Key), VersionId: aws.String(object.VersionID)})
			return err
		})
		if err != nil {
			cleanup = errors.Join(cleanup, fmt.Errorf("delete object %q version %q: %w", object.Key, object.VersionID, err))
		}
	}
	return true, cleanup
}

func UploadFailure(sourceRoot, bucket, prefix, objectKey string, primary, cleanup error) error {
	remoteRoot := "s3://" + bucket
	if prefix != "" {
		remoteRoot += "/" + prefix
	}
	parts := []string{fmt.Sprintf("s3 folder upload failed (source: %s, remote: %s", sourceRoot, remoteRoot)}
	if objectKey != "" {
		parts = append(parts, fmt.Sprintf(", object: %s", objectKey))
	}
	message := strings.Join(parts, "") + ")"
	return fmt.Errorf("%s: %w", message, errors.Join(primary, cleanup))
}
