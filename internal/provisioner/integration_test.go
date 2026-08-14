//go:build integration

package provisioner_test

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"seaweedfs-provisioner/internal/client"
	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/provisioner"
	"seaweedfs-provisioner/internal/s3config"
)

type testCluster struct {
	s3         *client.S3Client
	s3Endpoint string
	cleanup    func()
}

const (
	testAccessKey = "test-admin-key"
	testSecretKey = "test-admin-secret"

	// Where the generated s3.config is mounted inside the container.
	containerConfigPath = "/etc/seaweedfs/s3.json"
)

// adminIdentity is the identity the tests themselves authenticate with.
func adminIdentity() s3config.Identity {
	return s3config.Identity{
		Name:        "admin",
		Credentials: []s3config.Credential{{AccessKey: testAccessKey, SecretKey: testSecretKey}},
		Actions:     []string{"Admin", "Read", "Write", "List", "Tagging"},
	}
}

// writeS3Config renders an s3.config using the production writer and returns its
// path. Every integration test boots from a file produced this way, so a schema
// regression in the writer fails the whole suite rather than hiding until deploy.
func writeS3Config(t *testing.T, identities ...s3config.Identity) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "s3.config")
	store := s3config.NewStore(path, 0)

	for _, identity := range identities {
		if err := store.PutIdentity(context.Background(), identity); err != nil {
			t.Fatalf("writing identity %q: %v", identity.Name, err)
		}
	}
	return path
}

// bootSeaweedFS starts SeaweedFS with the given s3.config mounted.
func bootSeaweedFS(t *testing.T, s3ConfigPath string) testCluster {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "chrislusf/seaweedfs:latest",
		Cmd:          []string{"server", "-s3", "-filer", "-s3.config=" + containerConfigPath},
		ExposedPorts: []string{"8333/tcp", "8888/tcp"},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      s3ConfigPath,
				ContainerFilePath: containerConfigPath,
				// The image's entrypoint drops privileges to the `seaweed` user
				// before starting the gateway, so the file must be readable by it.
				FileMode: 0644,
			},
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("8333/tcp"),
			wait.ForListeningPort("8888/tcp"),
		).WithDeadline(3 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start seaweedfs container: %v", err)
	}

	// Registered before any t.Fatalf below, otherwise a failure to connect strands
	// the container until the reaper times out.
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("failed to terminate container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container host: %v", err)
	}

	s3Port, err := container.MappedPort(ctx, "8333")
	if err != nil {
		t.Fatalf("failed to get s3 port: %v", err)
	}

	s3Endpoint := "http://" + host + ":" + s3Port.Port()

	s3Client, err := client.ConnectS3(ctx, s3Endpoint, testAccessKey, testSecretKey, "us-east-1")
	if err != nil {
		t.Fatalf("failed to connect to s3: %v", err)
	}

	return testCluster{
		s3:         s3Client,
		s3Endpoint: s3Endpoint,
		// Termination is registered via t.Cleanup above; kept so existing callers
		// can still defer it without terminating twice.
		cleanup: func() {},
	}
}

// setupSeaweedFS boots a cluster whose only identity is the test admin.
func setupSeaweedFS(t *testing.T) testCluster {
	t.Helper()
	return bootSeaweedFS(t, writeS3Config(t, adminIdentity()))
}

// s3ClientAs builds a client authenticating as the given credentials. It bypasses
// client.ConnectS3 because that retries for five minutes, which would turn an
// authorization failure into an indistinguishable timeout.
func s3ClientAs(endpoint, accessKey, secretKey string) *client.S3Client {
	return &client.S3Client{Client: s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "us-east-1",
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	})}
}

func writeTestConfig(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadConfig parses provisioner YAML from a string.
func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()

	cfg, err := config.Load(writeTestConfig(t, yaml))
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return cfg
}

func TestIntegrationFullProvisioning(t *testing.T) {
	tc := setupSeaweedFS(t)
	defer tc.cleanup()

	cfg := loadConfig(t, `
buckets:
  - name: documents
  - name: media-assets
  - name: backups
`)

	ctx := context.Background()

	p := provisioner.New(tc.s3, nil, cfg)
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first provisioning run failed: %v", err)
	}

	// Verify buckets exist
	for _, name := range []string{"documents", "media-assets", "backups"} {
		exists, err := tc.s3.HeadBucket(ctx, name)
		if err != nil {
			t.Fatalf("checking bucket %q: %v", name, err)
		}
		if !exists {
			t.Errorf("bucket %q not found after provisioning", name)
		}
	}
}

func TestIntegrationIdempotency(t *testing.T) {
	tc := setupSeaweedFS(t)
	defer tc.cleanup()

	cfg := loadConfig(t, `
buckets:
  - name: idempotent-test
`)

	ctx := context.Background()

	if err := provisioner.New(tc.s3, nil, cfg).Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := provisioner.New(tc.s3, nil, cfg).Run(ctx); err != nil {
		t.Fatalf("second (idempotent) run: %v", err)
	}

	exists, err := tc.s3.HeadBucket(ctx, "idempotent-test")
	if err != nil {
		t.Fatalf("checking bucket: %v", err)
	}
	if !exists {
		t.Fatal("bucket not found after idempotent runs")
	}
}

func TestIntegrationPreExistingBucket(t *testing.T) {
	tc := setupSeaweedFS(t)
	defer tc.cleanup()

	ctx := context.Background()

	if err := tc.s3.CreateBucket(ctx, "pre-existing"); err != nil {
		t.Fatalf("creating pre-existing bucket: %v", err)
	}

	cfg := loadConfig(t, `
buckets:
  - name: pre-existing
`)

	if err := provisioner.New(tc.s3, nil, cfg).Run(ctx); err != nil {
		t.Fatalf("provisioning with pre-existing bucket: %v", err)
	}
}

func TestIntegrationEmptyConfig(t *testing.T) {
	tc := setupSeaweedFS(t)
	defer tc.cleanup()

	cfg := loadConfig(t, "{}")

	if err := provisioner.New(tc.s3, nil, cfg).Run(context.Background()); err != nil {
		t.Fatalf("empty config should succeed: %v", err)
	}
}

// repoRoot returns the module root, used to provision the checked-in example policy.
func repoRoot(t *testing.T) string {
	t.Helper()

	// integration_test.go lives at <root>/internal/provisioner.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// TestIntegrationBucketPolicy provisions the example policy shipped in the repo
// against a real cluster. It guards the documented quick start: a policy document
// SeaweedFS rejects would make `docker compose up` fail at the policy step.
func TestIntegrationBucketPolicy(t *testing.T) {
	tc := setupSeaweedFS(t)
	defer tc.cleanup()

	ctx := context.Background()

	// Policy file paths resolve against the working directory, so run from the
	// repo root where `policies/documents-policy.json` lives.
	t.Chdir(repoRoot(t))

	cfg := loadConfig(t, `
buckets:
  - name: documents

policies:
  - bucket: documents
    file: policies/documents-policy.json
`)

	p := provisioner.New(tc.s3, nil, cfg)
	if err := p.ProvisionCluster(ctx); err != nil {
		t.Fatalf("provisioning example policy failed: %v", err)
	}

	stored, err := tc.s3.GetBucketPolicy(ctx, "documents")
	if err != nil {
		t.Fatalf("reading back bucket policy: %v", err)
	}
	if stored == "" {
		t.Fatal("bucket policy was not persisted")
	}
	if !strings.Contains(stored, "AppServiceReadWriteDocuments") {
		t.Errorf("stored policy is not the one provisioned: %s", stored)
	}

	// Re-running must be a no-op rather than an error.
	if err := provisioner.New(tc.s3, nil, cfg).ProvisionCluster(ctx); err != nil {
		t.Fatalf("second (idempotent) policy run failed: %v", err)
	}
}

// TestIntegrationProvisionedIdentityAuthenticates exercises the full two-phase
// deployment: the provisioner writes s3.config before SeaweedFS exists, SeaweedFS
// boots from that file, and the provisioned identity is then able to authenticate
// against the S3 gateway. This is the end-to-end proof that the generated document
// matches the schema SeaweedFS actually loads.
func TestIntegrationProvisionedIdentityAuthenticates(t *testing.T) {
	ctx := context.Background()

	// Phase 1, pre-boot: no cluster exists yet.
	configPath := filepath.Join(t.TempDir(), "s3.config")
	store := s3config.NewStore(configPath, 0)

	if err := store.PutIdentity(ctx, adminIdentity()); err != nil {
		t.Fatalf("seeding admin identity: %v", err)
	}

	cfg := loadConfig(t, `
identities:
  - name: app-service
    credentials:
      - access_key: app-service-key
        secret_key: app-service-secret
    actions:
      - "Read"
      - "Write"
      - "List"
`)

	if err := provisioner.New(nil, store, cfg).ProvisionIdentities(ctx); err != nil {
		t.Fatalf("identity phase failed: %v", err)
	}

	// Phase 2: boot from the generated file, then provision cluster resources.
	tc := bootSeaweedFS(t, configPath)
	defer tc.cleanup()

	bucketCfg := loadConfig(t, `
buckets:
  - name: identity-access-test
`)
	if err := provisioner.New(tc.s3, nil, bucketCfg).ProvisionCluster(ctx); err != nil {
		t.Fatalf("cluster phase failed: %v", err)
	}

	// The provisioned identity must be able to authenticate in its own right.
	scoped := s3ClientAs(tc.s3Endpoint, "app-service-key", "app-service-secret")

	exists, err := scoped.HeadBucket(ctx, "identity-access-test")
	if err != nil {
		t.Fatalf("provisioned identity could not authenticate against S3: %v", err)
	}
	if !exists {
		t.Error("provisioned identity authenticated but could not see the bucket")
	}
}

// TestIntegrationIdentityMergePreservesAdmin guards the merge path against a real
// boot: an identity added to an existing s3.config must not displace the admin
// credentials already in it, or the operator is locked out of their own cluster.
func TestIntegrationIdentityMergePreservesAdmin(t *testing.T) {
	ctx := context.Background()

	configPath := filepath.Join(t.TempDir(), "s3.config")
	store := s3config.NewStore(configPath, 0)

	if err := store.PutIdentity(ctx, adminIdentity()); err != nil {
		t.Fatalf("seeding admin identity: %v", err)
	}

	cfg := loadConfig(t, `
identities:
  - name: extra-service
    credentials:
      - access_key: extra-key
        secret_key: extra-secret
    actions:
      - "Read"
`)
	if err := provisioner.New(nil, store, cfg).ProvisionIdentities(ctx); err != nil {
		t.Fatalf("identity phase failed: %v", err)
	}

	// bootSeaweedFS connects as the admin identity, so a clobbered admin surfaces
	// here as a connection failure.
	tc := bootSeaweedFS(t, configPath)
	defer tc.cleanup()

	if err := tc.s3.CreateBucket(ctx, "admin-still-works"); err != nil {
		t.Fatalf("admin identity no longer functional after merge: %v", err)
	}
}
