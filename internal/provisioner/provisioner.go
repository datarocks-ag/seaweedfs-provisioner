package provisioner

import (
	"context"
	"fmt"
	"log/slog"

	"seaweedfs-provisioner/internal/client"
	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/s3config"
)

// Options controls provisioner behaviour.
type Options struct {
	// DryRun, when true, logs every mutation as a preview instead of applying it.
	// Read-only calls (existence checks, current policy/versioning/lifecycle
	// lookups) still run so the preview reflects live state.
	DryRun bool
}

// DefaultOptions returns Options with sensible defaults.
func DefaultOptions() Options {
	return Options{}
}

// S3Admin defines the S3 operations needed by the provisioner.
type S3Admin interface {
	HeadBucket(ctx context.Context, name string) (bool, error)
	CreateBucket(ctx context.Context, name string) error
	GetBucketPolicy(ctx context.Context, name string) (string, error)
	PutBucketPolicy(ctx context.Context, name, policy string) error
	GetBucketVersioning(ctx context.Context, name string) (string, error)
	PutBucketVersioning(ctx context.Context, name string, enabled bool) error
	GetBucketLifecycle(ctx context.Context, name string) ([]client.LifecycleRule, error)
	PutBucketLifecycle(ctx context.Context, name string, rules []client.LifecycleRule) error
}

// IdentityStore defines the identity operations needed by the provisioner.
// Implementations merge each identity into the SeaweedFS s3.config document,
// leaving identities the provisioner does not manage untouched.
type IdentityStore interface {
	PutIdentity(ctx context.Context, identity s3config.Identity) error
}

// Provisioner orchestrates idempotent SeaweedFS resource provisioning.
//
// Work splits across two phases with opposing ordering requirements. Identities
// are written to the s3.config file the S3 gateway reads at boot, so they must
// be provisioned *before* SeaweedFS starts. Buckets and policies are reconciled
// over the S3 API and therefore need a *running* cluster. See ProvisionIdentities
// and ProvisionCluster.
type Provisioner struct {
	s3         S3Admin
	identities IdentityStore
	cfg        *config.Config
	opts       Options
}

// New creates a new Provisioner. Either dependency may be nil when only the
// other phase is going to be run.
func New(s3 S3Admin, identities IdentityStore, cfg *config.Config, opts Options) *Provisioner {
	return &Provisioner{
		s3:         s3,
		identities: identities,
		cfg:        cfg,
		opts:       opts,
	}
}

// ProvisionIdentities writes the configured identities into the s3.config store.
// It requires no running cluster and is the phase that runs before SeaweedFS boots.
func (p *Provisioner) ProvisionIdentities(ctx context.Context) error {
	if len(p.cfg.Identities) == 0 {
		slog.Debug("No identities configured, skipping identity phase")
		return nil
	}

	slog.Info("Provisioning identities", "count", len(p.cfg.Identities))

	for _, identity := range p.cfg.Identities {
		if err := p.ensureIdentity(ctx, identity); err != nil {
			return fmt.Errorf("provisioning identity %q: %w", identity.Name, err)
		}
	}

	return nil
}

// ProvisionCluster reconciles buckets and then policies against a running
// cluster. Policies are applied last because they reference buckets that must
// already exist.
func (p *Provisioner) ProvisionCluster(ctx context.Context) error {
	slog.Info("Provisioning cluster resources",
		"buckets", len(p.cfg.Buckets),
		"policies", len(p.cfg.Policies),
	)

	for _, bucket := range p.cfg.Buckets {
		strategy := config.EffectiveStrategy(bucket.Strategy, p.cfg.Strategy)
		if err := p.ensureBucket(ctx, bucket, strategy); err != nil {
			return fmt.Errorf("provisioning bucket %q: %w", bucket.Name, err)
		}
	}

	for _, policy := range p.cfg.Policies {
		if err := p.ensurePolicy(ctx, policy); err != nil {
			return fmt.Errorf("provisioning policy for bucket %q: %w", policy.Bucket, err)
		}
	}

	return nil
}

// Run executes both phases: identities, then cluster resources.
//
// Note that identities written here only take effect the next time SeaweedFS
// reads its s3.config, i.e. on restart. Use the separate phases when the
// provisioner runs as a pre-boot init container.
func (p *Provisioner) Run(ctx context.Context) error {
	slog.Info("Starting provisioning")

	if err := p.ProvisionIdentities(ctx); err != nil {
		return err
	}
	if err := p.ProvisionCluster(ctx); err != nil {
		return err
	}

	slog.Info("Provisioning complete")
	return nil
}
