package provisioner

import (
	"context"
	"fmt"
	"log/slog"

	"seaweedfs-provisioner/internal/config"
)

func (p *Provisioner) ensureBucket(ctx context.Context, bucket config.Bucket, strategy string) error {
	exists, err := p.s3.HeadBucket(ctx, bucket.Name)
	if err != nil {
		return fmt.Errorf("checking bucket: %w", err)
	}

	if exists {
		if strategy == "create" {
			slog.Info("Skipping existing bucket (strategy=create)", "bucket", bucket.Name)
		} else {
			slog.Debug("Bucket already exists", "bucket", bucket.Name)
		}
		return nil
	}

	slog.Info("Creating bucket", "bucket", bucket.Name)
	if err := p.s3.CreateBucket(ctx, bucket.Name); err != nil {
		return fmt.Errorf("creating bucket: %w", err)
	}

	return nil
}
