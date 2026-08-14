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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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

// LifecycleRule is a transport-neutral object expiration rule, so the
// provisioner does not have to depend on AWS SDK types.
type LifecycleRule struct {
	ID             string
	Prefix         string
	ExpirationDays int32
	Enabled        bool
}

// GetBucketVersioning returns "Enabled", "Suspended", or "" when versioning has
// never been configured on the bucket.
func (c *S3Client) GetBucketVersioning(ctx context.Context, name string) (string, error) {
	resp, err := c.Client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		return "", fmt.Errorf("getting versioning for %q: %w", name, err)
	}
	return string(resp.Status), nil
}

// PutBucketVersioning enables or suspends versioning on the bucket.
func (c *S3Client) PutBucketVersioning(ctx context.Context, name string, enabled bool) error {
	status := types.BucketVersioningStatusSuspended
	if enabled {
		status = types.BucketVersioningStatusEnabled
	}

	_, err := c.Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(name),
		VersioningConfiguration: &types.VersioningConfiguration{Status: status},
	})
	if err != nil {
		return fmt.Errorf("setting versioning for %q: %w", name, err)
	}
	return nil
}

// GetBucketLifecycle returns the bucket's expiration rules. A bucket with no
// lifecycle configuration yields no rules rather than an error.
func (c *S3Client) GetBucketLifecycle(ctx context.Context, name string) ([]LifecycleRule, error) {
	resp, err := c.Client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{
		Bucket: aws.String(name),
	})
	if err != nil {
		if isNotFoundError(err) || isNoSuchLifecycleError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting lifecycle for %q: %w", name, err)
	}

	rules := make([]LifecycleRule, 0, len(resp.Rules))
	for _, r := range resp.Rules {
		rule := LifecycleRule{
			ID:      aws.ToString(r.ID),
			Enabled: r.Status == types.ExpirationStatusEnabled,
		}
		if r.Expiration != nil {
			rule.ExpirationDays = aws.ToInt32(r.Expiration.Days)
		}
		rule.Prefix = lifecyclePrefix(r)
		rules = append(rules, rule)
	}
	return rules, nil
}

// lifecyclePrefix reads the rule prefix from either the modern filter or the
// deprecated top-level field, since SeaweedFS may return either shape.
func lifecyclePrefix(r types.LifecycleRule) string {
	if r.Filter != nil && r.Filter.Prefix != nil {
		return *r.Filter.Prefix
	}
	// Deprecated in the S3 API, but SeaweedFS still returns this shape, and
	// ignoring it would make every run see drift and rewrite the configuration.
	//nolint:staticcheck // intentionally reading the legacy top-level Prefix
	return aws.ToString(r.Prefix)
}

// PutBucketLifecycle replaces the bucket's lifecycle configuration.
func (c *S3Client) PutBucketLifecycle(ctx context.Context, name string, rules []LifecycleRule) error {
	converted := make([]types.LifecycleRule, 0, len(rules))
	for _, r := range rules {
		status := types.ExpirationStatusDisabled
		if r.Enabled {
			status = types.ExpirationStatusEnabled
		}
		converted = append(converted, types.LifecycleRule{
			ID:         aws.String(r.ID),
			Status:     status,
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(r.Prefix)},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(r.ExpirationDays)},
		})
	}

	_, err := c.Client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(name),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: converted},
	})
	if err != nil {
		return fmt.Errorf("setting lifecycle for %q: %w", name, err)
	}
	return nil
}

// isNoSuchLifecycleError checks if the error indicates no lifecycle is configured.
func isNoSuchLifecycleError(err error) bool {
	return strings.Contains(err.Error(), "NoSuchLifecycleConfiguration")
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
