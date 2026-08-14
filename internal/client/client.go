package client

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	maxRetries   = 15
	initialDelay = 1 * time.Second
	maxDelay     = 30 * time.Second
	totalTimeout = 5 * time.Minute
)

// S3Client wraps an AWS S3 client for bucket and policy operations.
type S3Client struct {
	Client *s3.Client
}

// ConnectS3 establishes an S3 connection with exponential backoff retry.
func ConnectS3(ctx context.Context, endpoint, accessKey, secretKey, region string) (*S3Client, error) {
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	opts := s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		UsePathStyle: true,
	}
	if accessKey != "" && secretKey != "" {
		opts.Credentials = credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")
	} else {
		opts.Credentials = aws.AnonymousCredentials{}
	}

	client := s3.New(opts)

	sc := &S3Client{Client: client}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		_, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
		if err == nil {
			slog.Info("Connected to SeaweedFS S3", "endpoint", endpoint)
			return sc, nil
		}

		if ctx.Err() != nil {
			return nil, fmt.Errorf("connection timeout after %s: %w", totalTimeout, ctx.Err())
		}

		delay := time.Duration(float64(initialDelay) * math.Pow(2, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}

		slog.Warn("SeaweedFS S3 not ready, retrying",
			"attempt", attempt+1,
			"max_retries", maxRetries,
			"delay", delay,
			"error", err,
		)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, fmt.Errorf("connection timeout: %w", ctx.Err())
		}
	}

	return nil, fmt.Errorf("failed to connect after %d retries", maxRetries+1)
}

// HeadBucket checks whether a bucket exists. Returns true if it exists, false otherwise.
func (c *S3Client) HeadBucket(ctx context.Context, name string) (bool, error) {
	_, err := c.Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		// HeadBucket returns an error for non-existent buckets
		if isNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("head bucket %q: %w", name, err)
	}
	return true, nil
}

// CreateBucket creates a new S3 bucket.
func (c *S3Client) CreateBucket(ctx context.Context, name string) error {
	_, err := c.Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		return fmt.Errorf("creating bucket %q: %w", name, err)
	}
	return nil
}

// GetBucketPolicy returns the bucket policy JSON string, or empty string if not set.
func (c *S3Client) GetBucketPolicy(ctx context.Context, name string) (string, error) {
	resp, err := c.Client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		if isNotFoundError(err) || isNoSuchPolicyError(err) {
			return "", nil
		}
		return "", fmt.Errorf("getting bucket policy for %q: %w", name, err)
	}
	if resp.Policy == nil {
		return "", nil
	}
	return *resp.Policy, nil
}

// PutBucketPolicy sets the bucket policy.
func (c *S3Client) PutBucketPolicy(ctx context.Context, name, policy string) error {
	_, err := c.Client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(name),
		Policy: aws.String(policy),
	})
	if err != nil {
		return fmt.Errorf("putting bucket policy for %q: %w", name, err)
	}
	return nil
}

// isNotFoundError checks if the error is a 404/NotFound type.
func isNotFoundError(err error) bool {
	return strings.Contains(err.Error(), "NotFound") ||
		strings.Contains(err.Error(), "404") ||
		strings.Contains(err.Error(), "NoSuchBucket")
}

// isNoSuchPolicyError checks if the error indicates no policy is set.
func isNoSuchPolicyError(err error) bool {
	return strings.Contains(err.Error(), "NoSuchBucketPolicy") ||
		strings.Contains(err.Error(), "NoSuchPolicy") ||
		strings.Contains(err.Error(), "The bucket policy does not exist")
}
