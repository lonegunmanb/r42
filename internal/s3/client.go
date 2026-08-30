package s3

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
)

// Client is the S3 operation subset used by folder uploads.
type Client interface {
	GetBucketVersioningWithContext(aws.Context, *awss3.GetBucketVersioningInput, ...request.Option) (*awss3.GetBucketVersioningOutput, error)
	PutObjectWithContext(aws.Context, *awss3.PutObjectInput, ...request.Option) (*awss3.PutObjectOutput, error)
	CreateMultipartUploadWithContext(aws.Context, *awss3.CreateMultipartUploadInput, ...request.Option) (*awss3.CreateMultipartUploadOutput, error)
	UploadPartWithContext(aws.Context, *awss3.UploadPartInput, ...request.Option) (*awss3.UploadPartOutput, error)
	CompleteMultipartUploadWithContext(aws.Context, *awss3.CompleteMultipartUploadInput, ...request.Option) (*awss3.CompleteMultipartUploadOutput, error)
	AbortMultipartUploadWithContext(aws.Context, *awss3.AbortMultipartUploadInput, ...request.Option) (*awss3.AbortMultipartUploadOutput, error)
	DeleteObjectWithContext(aws.Context, *awss3.DeleteObjectInput, ...request.Option) (*awss3.DeleteObjectOutput, error)
}

type EnvLookup func(string) (string, bool)

type ServiceFactory func(*aws.Config) (Client, error)

func NewClient(config s3spec.ProviderConfig, lookup EnvLookup, factory ServiceFactory) (Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	awsConfig := aws.NewConfig().WithRegion(config.Region).WithS3ForcePathStyle(config.ForcePathStyle).WithMaxRetries(0)
	if config.Endpoint != "" {
		awsConfig = awsConfig.WithEndpoint(config.Endpoint)
	}
	accessKey, secretKey, sessionToken, err := materializeCredentials(config, lookup)
	if err != nil {
		return nil, err
	}
	if accessKey != "" {
		awsConfig = awsConfig.WithCredentials(credentials.NewStaticCredentials(accessKey, secretKey, sessionToken))
	}
	if factory == nil {
		factory = newAWSClient
	}
	return factory(awsConfig)
}

func newAWSClient(config *aws.Config) (Client, error) {
	sessionValue, err := session.NewSession(config)
	if err != nil {
		return nil, fmt.Errorf("create S3 session: %w", err)
	}
	return awss3.New(sessionValue), nil
}

func materializeCredentials(config s3spec.ProviderConfig, lookup EnvLookup) (string, string, string, error) {
	accessKey, err := materializeCredential("access key", config.AccessKey, config.AccessKeyRef, lookup)
	if err != nil {
		return "", "", "", err
	}
	secretKey, err := materializeCredential("secret key", config.SecretKey, config.SecretKeyRef, lookup)
	if err != nil {
		return "", "", "", err
	}
	sessionToken, err := materializeCredential("session token", config.SessionToken, config.SessionTokenRef, lookup)
	if err != nil {
		return "", "", "", err
	}
	return accessKey, secretKey, sessionToken, nil
}

func materializeCredential(name string, value, reference *string, lookup EnvLookup) (string, error) {
	if value != nil {
		return *value, nil
	}
	if reference == nil {
		return "", nil
	}
	if lookup != nil {
		if value, found := lookup(*reference); found && value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("S3 %s reference %q is not set or empty", name, *reference)
}
