package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/s3config"
)

func TestEnvOrDefault(t *testing.T) {
	t.Run("returns the default when unset", func(t *testing.T) {
		t.Setenv("SEAWEEDFS_TEST_VALUE", "")
		if got := envOrDefault("SEAWEEDFS_TEST_VALUE", "fallback"); got != "fallback" {
			t.Errorf("got %q, want %q", got, "fallback")
		}
	})

	t.Run("returns the env value when set", func(t *testing.T) {
		t.Setenv("SEAWEEDFS_TEST_VALUE", "configured")
		if got := envOrDefault("SEAWEEDFS_TEST_VALUE", "fallback"); got != "configured" {
			t.Errorf("got %q, want %q", got, "configured")
		}
	})
}

func TestSetupLogging(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unrecognised"} {
		t.Run(level, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", level)
			setupLogging() // must not panic on any value, including unknown ones
		})
	}
}

func TestResolveConfigMode(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    os.FileMode
		wantErr bool
	}{
		{"unset falls back to the default", "", s3config.DefaultFileMode, false},
		{"explicit 0640", "0640", 0o640, false},
		{"explicit 0600", "0600", 0o600, false},
		{"bare octal without leading zero", "644", 0o644, false},
		{"non-numeric", "not-a-mode", 0, true},
		{"invalid octal digit", "0999", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEAWEEDFS_S3_CONFIG_MODE", tt.env)

			got, err := resolveConfigMode()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tt.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("mode = %04o, want %04o", got, tt.want)
			}
		})
	}
}

func TestNewIdentityStoreWithoutIdentitiesNeedsNoStore(t *testing.T) {
	t.Setenv("SEAWEEDFS_S3_CONFIG_PATH", "")

	store, err := newIdentityStore(&config.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store != nil {
		t.Error("expected no store when no identities are configured")
	}
}

// Silently skipping identities because the path is unset would look like a
// successful run that provisioned nothing.
func TestNewIdentityStoreRequiresPathWhenIdentitiesConfigured(t *testing.T) {
	t.Setenv("SEAWEEDFS_S3_CONFIG_PATH", "")

	cfg := &config.Config{Identities: []config.Identity{{Name: "app-service"}}}

	_, err := newIdentityStore(cfg)
	if err == nil {
		t.Fatal("expected an error when the s3.config path is unset")
	}
	if !strings.Contains(err.Error(), "SEAWEEDFS_S3_CONFIG_PATH") {
		t.Errorf("error should name the missing variable, got: %v", err)
	}
}

func TestNewIdentityStoreRejectsInvalidMode(t *testing.T) {
	t.Setenv("SEAWEEDFS_S3_CONFIG_PATH", filepath.Join(t.TempDir(), "s3.config"))
	t.Setenv("SEAWEEDFS_S3_CONFIG_MODE", "not-a-mode")

	cfg := &config.Config{Identities: []config.Identity{{Name: "app-service"}}}

	if _, err := newIdentityStore(cfg); err == nil {
		t.Fatal("expected an error for an invalid file mode")
	}
}

func TestRunRejectsMissingConfigFile(t *testing.T) {
	t.Setenv("SEAWEEDFS_CONFIG_PATH", filepath.Join(t.TempDir(), "absent.yaml"))

	if err := run(context.Background(), phaseIdentities); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

// The identity phase must complete without any cluster reachable — that is the
// entire point of running it before SeaweedFS starts.
func TestRunIdentitiesPhaseNeedsNoCluster(t *testing.T) {
	dir := t.TempDir()

	configPath := filepath.Join(dir, "config.yaml")
	configYAML := `
identities:
  - name: app-service
    credentials:
      - access_key: ${TEST_ACCESS_KEY}
        secret_key: ${TEST_SECRET_KEY}
    actions:
      - "Read"
      - "Write"
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	s3ConfigPath := filepath.Join(dir, "s3.config")

	t.Setenv("SEAWEEDFS_CONFIG_PATH", configPath)
	t.Setenv("SEAWEEDFS_S3_CONFIG_PATH", s3ConfigPath)
	t.Setenv("SEAWEEDFS_S3_CONFIG_MODE", "")
	t.Setenv("TEST_ACCESS_KEY", "expanded-key")
	t.Setenv("TEST_SECRET_KEY", "expanded-secret")
	// Deliberately unreachable: reaching for it would fail the test rather than hang,
	// because the identity phase must never construct an S3 client.
	t.Setenv("SEAWEEDFS_S3_ENDPOINT", "http://127.0.0.1:1")

	if err := run(context.Background(), phaseIdentities); err != nil {
		t.Fatalf("identity phase failed: %v", err)
	}

	data, err := os.ReadFile(s3ConfigPath)
	if err != nil {
		t.Fatalf("s3.config was not written: %v", err)
	}

	var doc struct {
		Identities []s3config.Identity `json:"identities"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("s3.config is not valid JSON: %v", err)
	}
	if len(doc.Identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(doc.Identities))
	}
	if doc.Identities[0].Name != "app-service" {
		t.Errorf("name = %q, want app-service", doc.Identities[0].Name)
	}
	// ${VAR} expansion has to survive the whole path from YAML to rendered file.
	if got := doc.Identities[0].Credentials[0].AccessKey; got != "expanded-key" {
		t.Errorf("access key = %q, want the expanded value", got)
	}
}

// An empty config in the cluster phase still needs a reachable endpoint, so a
// dead one must surface as an error rather than silently succeeding.
func TestRunClusterPhaseRequiresReachableEndpoint(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SEAWEEDFS_CONFIG_PATH", configPath)
	t.Setenv("SEAWEEDFS_S3_ENDPOINT", "http://127.0.0.1:1")

	// Cancelled up front so the retry loop exits immediately instead of
	// burning the full connection budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, phaseCluster); err == nil {
		t.Fatal("expected an error when the endpoint is unreachable")
	}
}
