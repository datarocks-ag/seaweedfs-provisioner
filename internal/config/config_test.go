package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	yaml := `
buckets:
  - name: documents
  - name: media-assets

identities:
  - name: app-service
    credentials:
      - access_key: AKID1
        secret_key: SECRET1
    actions:
      - "Read"
      - "Write"

policies:
  - bucket: documents
    file: policies/documents.json
`
	path := writeTempConfig(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.Buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(cfg.Buckets))
	}
	if cfg.Buckets[0].Name != "documents" {
		t.Errorf("expected bucket name 'documents', got %q", cfg.Buckets[0].Name)
	}

	if len(cfg.Identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(cfg.Identities))
	}
	if cfg.Identities[0].Name != "app-service" {
		t.Errorf("expected identity name 'app-service', got %q", cfg.Identities[0].Name)
	}

	if len(cfg.Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(cfg.Policies))
	}
	if cfg.Policies[0].Bucket != "documents" {
		t.Errorf("expected policy bucket 'documents', got %q", cfg.Policies[0].Bucket)
	}
}

func TestLoadEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_ACCESS_KEY", "mykey123")
	t.Setenv("TEST_SECRET_KEY", "mysecret456")
	t.Setenv("TEST_BUCKET", "my-bucket")

	yaml := `
buckets:
  - name: ${TEST_BUCKET}

identities:
  - name: svc
    credentials:
      - access_key: ${TEST_ACCESS_KEY}
        secret_key: ${TEST_SECRET_KEY}
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Buckets[0].Name != "my-bucket" {
		t.Errorf("expected bucket name 'my-bucket', got %q", cfg.Buckets[0].Name)
	}
	if cfg.Identities[0].Credentials[0].AccessKey != "mykey123" {
		t.Errorf("expected access key 'mykey123', got %q", cfg.Identities[0].Credentials[0].AccessKey)
	}
	if cfg.Identities[0].Credentials[0].SecretKey != "mysecret456" {
		t.Errorf("expected secret key 'mysecret456', got %q", cfg.Identities[0].Credentials[0].SecretKey)
	}
}

func TestLoadUnresolvedEnvVar(t *testing.T) {
	os.Unsetenv("NONEXISTENT_VAR")

	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: ${NONEXISTENT_VAR}
        secret_key: somesecret
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Identities[0].Credentials[0].AccessKey != "${NONEXISTENT_VAR}" {
		t.Errorf("expected unresolved env var to be kept as-is, got %q", cfg.Identities[0].Credentials[0].AccessKey)
	}
}

func TestValidateBucketNameRequired(t *testing.T) {
	yaml := `
buckets:
  - name: ""
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing bucket name")
	}
}

func TestValidateBucketNameTooShort(t *testing.T) {
	yaml := `
buckets:
  - name: ab
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for bucket name too short")
	}
}

func TestValidateBucketNameInvalid(t *testing.T) {
	yaml := `
buckets:
  - name: My_Bucket!
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid bucket name")
	}
}

func TestValidateDuplicateBucketName(t *testing.T) {
	yaml := `
buckets:
  - name: documents
  - name: documents
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate bucket name")
	}
}

func TestValidateIdentityNameRequired(t *testing.T) {
	yaml := `
identities:
  - credentials:
      - access_key: key
        secret_key: secret
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing identity name")
	}
}

func TestValidateDuplicateIdentityName(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: key1
        secret_key: secret1
    actions:
      - "Read"
  - name: svc
    credentials:
      - access_key: key2
        secret_key: secret2
    actions:
      - "Write"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate identity name")
	}
}

func TestValidateIdentityMissingCredentials(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials: []
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestValidateIdentityMissingAccessKey(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: ""
        secret_key: secret
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing access key")
	}
}

func TestValidateIdentityMissingSecretKey(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: key
        secret_key: ""
    actions:
      - "Read"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing secret key")
	}
}

func TestValidateIdentityMissingActions(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: key
        secret_key: secret
    actions: []
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing actions")
	}
}

func TestValidateIdentityInvalidAction(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: key
        secret_key: secret
    actions:
      - "Delete"
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestValidateIdentityScopedAction(t *testing.T) {
	yaml := `
identities:
  - name: svc
    credentials:
      - access_key: key
        secret_key: secret
    actions:
      - "Read:my-bucket"
      - "Write:my-bucket"
      - "List:my-bucket"
`
	path := writeTempConfig(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Identities[0].Actions) != 3 {
		t.Errorf("expected 3 actions, got %d", len(cfg.Identities[0].Actions))
	}
}

func TestValidatePolicyBucketRequired(t *testing.T) {
	yaml := `
policies:
  - file: policy.json
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing policy bucket")
	}
}

func TestValidatePolicyFileRequired(t *testing.T) {
	yaml := `
policies:
  - bucket: documents
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing policy file")
	}
}

func TestValidatePolicyDuplicateBucket(t *testing.T) {
	yaml := `
policies:
  - bucket: documents
    file: a.json
  - bucket: documents
    file: b.json
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate policy bucket")
	}
}

func TestValidateInvalidStrategy(t *testing.T) {
	yaml := `
strategy: "delete"
buckets:
  - name: documents
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid strategy")
	}
}

func TestValidateBucketInvalidStrategy(t *testing.T) {
	yaml := `
buckets:
  - name: documents
    strategy: delete
`
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid per-bucket strategy")
	}
}

func TestValidateNullByteBucketName(t *testing.T) {
	yaml := "buckets:\n  - name: \"doc\\x00uments\"\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in bucket name")
	}
}

func TestValidateNullByteIdentityName(t *testing.T) {
	yaml := "identities:\n  - name: \"svc\\x00name\"\n    credentials:\n      - access_key: key\n        secret_key: secret\n    actions:\n      - \"Read\"\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in identity name")
	}
}

func TestValidateNullByteAccessKey(t *testing.T) {
	yaml := "identities:\n  - name: svc\n    credentials:\n      - access_key: \"key\\x00val\"\n        secret_key: secret\n    actions:\n      - \"Read\"\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in access key")
	}
}

func TestValidateNullByteSecretKey(t *testing.T) {
	yaml := "identities:\n  - name: svc\n    credentials:\n      - access_key: key\n        secret_key: \"sec\\x00ret\"\n    actions:\n      - \"Read\"\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in secret key")
	}
}

func TestValidateNullBytePolicyBucket(t *testing.T) {
	yaml := "policies:\n  - bucket: \"doc\\x00s\"\n    file: policy.json\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in policy bucket")
	}
}

func TestValidateNullBytePolicyFile(t *testing.T) {
	yaml := "policies:\n  - bucket: documents\n    file: \"pol\\x00icy.json\"\n"
	path := writeTempConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for null byte in policy file")
	}
}

func TestEffectiveStrategy(t *testing.T) {
	tests := []struct {
		strategies []string
		want       string
	}{
		{[]string{"", ""}, "update"},
		{[]string{"create", ""}, "create"},
		{[]string{"", "create"}, "create"},
		{[]string{"update", "create"}, "update"},
	}

	for _, tt := range tests {
		got := EffectiveStrategy(tt.strategies...)
		if got != tt.want {
			t.Errorf("EffectiveStrategy(%v) = %q, want %q", tt.strategies, got, tt.want)
		}
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	yaml := `
buckets:
  - name: [invalid yaml
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadEmptyConfig(t *testing.T) {
	path := writeTempConfig(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Buckets) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(cfg.Buckets))
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}
