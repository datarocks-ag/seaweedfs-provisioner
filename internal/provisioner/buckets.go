package provisioner

import (
	"context"
	"fmt"
	"log/slog"

	"seaweedfs-provisioner/internal/client"
	"seaweedfs-provisioner/internal/config"
)

// versioningEnabled is the status SeaweedFS reports for an enabled bucket.
const versioningEnabled = "Enabled"

func (p *Provisioner) ensureBucket(ctx context.Context, bucket config.Bucket, strategy string) error {
	exists, err := p.s3.HeadBucket(ctx, bucket.Name)
	if err != nil {
		return fmt.Errorf("checking bucket: %w", err)
	}

	if !exists {
		if p.opts.DryRun {
			// Settings cannot be reconciled against a bucket that does not exist
			// yet, so report the intent rather than reading live state.
			slog.Info("Would create bucket", "bucket", bucket.Name)
			p.previewSettingsForNewBucket(bucket)
			return nil
		}

		slog.Info("Creating bucket", "bucket", bucket.Name)
		if err := p.s3.CreateBucket(ctx, bucket.Name); err != nil {
			return fmt.Errorf("creating bucket: %w", err)
		}
	} else if strategy == "create" {
		// Matching postgres-provisioner: "create" on an existing resource skips
		// its sub-resources too.
		slog.Info("Skipping existing bucket and its settings (strategy=create)", "bucket", bucket.Name)
		return nil
	} else {
		slog.Debug("Bucket already exists", "bucket", bucket.Name)
	}

	if err := p.ensureVersioning(ctx, bucket); err != nil {
		return fmt.Errorf("reconciling versioning: %w", err)
	}
	if err := p.ensureLifecycle(ctx, bucket); err != nil {
		return fmt.Errorf("reconciling lifecycle: %w", err)
	}

	return nil
}

// previewSettingsForNewBucket logs the settings that would follow a create.
func (p *Provisioner) previewSettingsForNewBucket(bucket config.Bucket) {
	if bucket.Versioning != nil {
		slog.Info("Would set bucket versioning", "bucket", bucket.Name, "enabled", *bucket.Versioning)
	}
	if len(bucket.Lifecycle) > 0 {
		slog.Info("Would set bucket lifecycle", "bucket", bucket.Name, "rules", len(bucket.Lifecycle))
	}
}

// ensureVersioning reconciles the bucket's versioning status. An omitted
// `versioning` field leaves whatever is configured on the bucket alone.
func (p *Provisioner) ensureVersioning(ctx context.Context, bucket config.Bucket) error {
	if bucket.Versioning == nil {
		return nil
	}
	desired := *bucket.Versioning

	current, err := p.s3.GetBucketVersioning(ctx, bucket.Name)
	if err != nil {
		return fmt.Errorf("getting current versioning: %w", err)
	}

	if (current == versioningEnabled) == desired {
		slog.Debug("Bucket versioning up to date", "bucket", bucket.Name, "enabled", desired)
		return nil
	}

	if p.opts.DryRun {
		slog.Info("Would set bucket versioning",
			"bucket", bucket.Name, "current", current, "enabled", desired)
		return nil
	}

	slog.Info("Setting bucket versioning", "bucket", bucket.Name, "enabled", desired)
	if err := p.s3.PutBucketVersioning(ctx, bucket.Name, desired); err != nil {
		return fmt.Errorf("setting versioning: %w", err)
	}
	return nil
}

// ensureLifecycle reconciles the bucket's expiration rules. An empty
// `lifecycle` list leaves any existing configuration alone rather than
// clearing it, keeping the provisioner additive.
func (p *Provisioner) ensureLifecycle(ctx context.Context, bucket config.Bucket) error {
	if len(bucket.Lifecycle) == 0 {
		return nil
	}

	current, err := p.s3.GetBucketLifecycle(ctx, bucket.Name)
	if err != nil {
		return fmt.Errorf("getting current lifecycle: %w", err)
	}

	desired := mergeLifecycleRules(current, bucket.Lifecycle)

	if lifecycleRulesEqual(current, desired) {
		slog.Debug("Bucket lifecycle up to date", "bucket", bucket.Name, "rules", len(current))
		return nil
	}

	if p.opts.DryRun {
		slog.Info("Would set bucket lifecycle",
			"bucket", bucket.Name, "current_rules", len(current), "desired_rules", len(desired))
		return nil
	}

	slog.Info("Setting bucket lifecycle", "bucket", bucket.Name, "rules", len(desired))
	if err := p.s3.PutBucketLifecycle(ctx, bucket.Name, desired); err != nil {
		return fmt.Errorf("setting lifecycle: %w", err)
	}
	return nil
}

// mergeLifecycleRules overlays the configured rules onto the bucket's current
// ones, matching by rule ID. PutBucketLifecycle replaces the whole document, so
// rules the provisioner does not manage have to be carried across or they would
// be deleted.
func mergeLifecycleRules(current []client.LifecycleRule, configured []config.LifecycleRule) []client.LifecycleRule {
	merged := make([]client.LifecycleRule, len(current))
	copy(merged, current)

	for _, c := range configured {
		rule := client.LifecycleRule{
			ID:             c.ID,
			Prefix:         c.Prefix,
			ExpirationDays: int32(c.ExpirationDays),
			Enabled:        c.Enabled(),
		}

		replaced := false
		for i := range merged {
			if merged[i].ID == rule.ID {
				merged[i] = rule
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, rule)
		}
	}

	return merged
}

// lifecycleRulesEqual compares rule sets order-independently, since the gateway
// is free to return them in any order.
func lifecycleRulesEqual(a, b []client.LifecycleRule) bool {
	if len(a) != len(b) {
		return false
	}

	index := make(map[string]client.LifecycleRule, len(a))
	for _, r := range a {
		index[r.ID] = r
	}

	for _, r := range b {
		existing, ok := index[r.ID]
		if !ok || existing != r {
			return false
		}
	}
	return true
}
