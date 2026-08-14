package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"seaweedfs-provisioner/internal/client"
	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/provisioner"
	"seaweedfs-provisioner/internal/s3config"
)

var version = "dev"

// Provisioning phases. Identities are written to the s3.config file SeaweedFS
// reads at boot, so they must be provisioned before it starts; buckets and
// policies go over the S3 API and need it running. Splitting the phases lets a
// deployment run one pass as a pre-boot init container and another afterwards.
const (
	phaseAll        = "all"
	phaseIdentities = "identities"
	phaseCluster    = "cluster"
)

func main() {
	phaseFlag := flag.String("phase", "",
		`Provisioning phase: "identities" (write s3.config, no cluster needed), `+
			`"cluster" (buckets and policies, needs a running cluster), or "all" (default). `+
			`Overrides SEAWEEDFS_PHASE.`)
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	setupLogging()
	slog.Info("Starting seaweedfs-provisioner", "version", version)

	// The CLI flag takes precedence over the env var, matching the sibling provisioners.
	phase := *phaseFlag
	if phase == "" {
		phase = envOrDefault("SEAWEEDFS_PHASE", phaseAll)
	}
	if phase != phaseAll && phase != phaseIdentities && phase != phaseCluster {
		slog.Error("Invalid phase",
			"phase", phase,
			"valid", []string{phaseAll, phaseIdentities, phaseCluster},
		)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, phase); err != nil {
		slog.Error("Provisioning failed", "error", err)
		os.Exit(1)
	}

	slog.Info("seaweedfs-provisioner finished successfully")
}

func run(ctx context.Context, phase string) error {
	configPath := envOrDefault("SEAWEEDFS_CONFIG_PATH", "./config.yaml")

	slog.Info("Loading configuration", "path", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	slog.Info("Configuration loaded",
		"phase", phase,
		"buckets", len(cfg.Buckets),
		"identities", len(cfg.Identities),
		"policies", len(cfg.Policies),
	)

	var identities provisioner.IdentityStore
	if phase == phaseAll || phase == phaseIdentities {
		identities, err = newIdentityStore(cfg)
		if err != nil {
			return err
		}
	}

	// Declared as the interface rather than *client.S3Client so that skipping the
	// cluster phase leaves a nil interface instead of a non-nil interface wrapping
	// a nil pointer.
	var s3Admin provisioner.S3Admin
	if phase == phaseAll || phase == phaseCluster {
		s3Client, err := connectS3(ctx)
		if err != nil {
			return err
		}
		s3Admin = s3Client
	}

	p := provisioner.New(s3Admin, identities, cfg)

	switch phase {
	case phaseIdentities:
		return p.ProvisionIdentities(ctx)
	case phaseCluster:
		return p.ProvisionCluster(ctx)
	default:
		return p.Run(ctx)
	}
}

// newIdentityStore builds the s3.config store, or returns nil when the config
// declares no identities and therefore needs no store.
func newIdentityStore(cfg *config.Config) (provisioner.IdentityStore, error) {
	if len(cfg.Identities) == 0 {
		return nil, nil
	}

	path := os.Getenv("SEAWEEDFS_S3_CONFIG_PATH")
	if path == "" {
		return nil, errors.New(
			"SEAWEEDFS_S3_CONFIG_PATH is required when identities are configured: " +
				"identities are provisioned by writing the s3.config file SeaweedFS loads via -s3.config")
	}

	mode, err := resolveConfigMode()
	if err != nil {
		return nil, err
	}

	slog.Info("Using s3.config for identities", "path", path, "mode", fmt.Sprintf("%04o", mode))
	return s3config.NewStore(path, mode), nil
}

// resolveConfigMode reads the octal file mode for the generated s3.config.
// SeaweedFS reads the file as an unprivileged user, so deployments that can
// align uids may want something tighter than the world-readable default.
func resolveConfigMode() (os.FileMode, error) {
	raw := os.Getenv("SEAWEEDFS_S3_CONFIG_MODE")
	if raw == "" {
		return s3config.DefaultFileMode, nil
	}

	parsed, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing SEAWEEDFS_S3_CONFIG_MODE %q as an octal file mode: %w", raw, err)
	}
	return os.FileMode(parsed), nil
}

func connectS3(ctx context.Context) (*client.S3Client, error) {
	endpoint := envOrDefault("SEAWEEDFS_S3_ENDPOINT", "http://localhost:8333")
	accessKey := os.Getenv("SEAWEEDFS_ACCESS_KEY")
	secretKey := os.Getenv("SEAWEEDFS_SECRET_KEY")
	region := envOrDefault("SEAWEEDFS_REGION", "us-east-1")

	slog.Info("Connecting to SeaweedFS S3", "endpoint", endpoint)
	s3Client, err := client.ConnectS3(ctx, endpoint, accessKey, secretKey, region)
	if err != nil {
		return nil, fmt.Errorf("connecting to SeaweedFS S3: %w", err)
	}
	return s3Client, nil
}

func setupLogging() {
	level := slog.LevelInfo
	switch envOrDefault("LOG_LEVEL", "info") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

func envOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
