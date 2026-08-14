package provisioner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"seaweedfs-provisioner/internal/config"
)

func (p *Provisioner) ensurePolicy(ctx context.Context, policy config.Policy) error {
	policyData, err := os.ReadFile(policy.File)
	if err != nil {
		return fmt.Errorf("reading policy file %q: %w", policy.File, err)
	}

	if !json.Valid(policyData) {
		return fmt.Errorf("policy file %q is not valid JSON", policy.File)
	}

	desiredPolicy := string(policyData)

	currentPolicy, err := p.s3.GetBucketPolicy(ctx, policy.Bucket)
	if err != nil {
		return fmt.Errorf("getting current policy: %w", err)
	}

	if currentPolicy != "" && normalizeJSON(currentPolicy) == normalizeJSON(desiredPolicy) {
		slog.Debug("Bucket policy up to date", "bucket", policy.Bucket)
		return nil
	}

	if p.opts.DryRun {
		slog.Info("Would set bucket policy", "bucket", policy.Bucket, "file", policy.File)
		return nil
	}

	slog.Info("Setting bucket policy", "bucket", policy.Bucket, "file", policy.File)
	if err := p.s3.PutBucketPolicy(ctx, policy.Bucket, desiredPolicy); err != nil {
		return fmt.Errorf("setting bucket policy: %w", err)
	}

	return nil
}

// normalizeJSON compacts JSON to remove whitespace differences.
func normalizeJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}
