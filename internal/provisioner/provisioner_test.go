package provisioner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/s3config"
)

// mockS3Admin implements S3Admin for unit tests.
type mockS3Admin struct {
	headBucketFn      func(ctx context.Context, name string) (bool, error)
	createBucketFn    func(ctx context.Context, name string) error
	getBucketPolicyFn func(ctx context.Context, name string) (string, error)
	putBucketPolicyFn func(ctx context.Context, name, policy string) error
}

func (m *mockS3Admin) HeadBucket(ctx context.Context, name string) (bool, error) {
	return m.headBucketFn(ctx, name)
}
func (m *mockS3Admin) CreateBucket(ctx context.Context, name string) error {
	return m.createBucketFn(ctx, name)
}
func (m *mockS3Admin) GetBucketPolicy(ctx context.Context, name string) (string, error) {
	return m.getBucketPolicyFn(ctx, name)
}
func (m *mockS3Admin) PutBucketPolicy(ctx context.Context, name, policy string) error {
	return m.putBucketPolicyFn(ctx, name, policy)
}

// mockIdentityStore implements IdentityStore for unit tests.
type mockIdentityStore struct {
	putIdentityFn func(ctx context.Context, identity s3config.Identity) error
}

func (m *mockIdentityStore) PutIdentity(ctx context.Context, identity s3config.Identity) error {
	return m.putIdentityFn(ctx, identity)
}

func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// --- Run tests ---

func TestRun_EmptyConfig(t *testing.T) {
	p := New(&mockS3Admin{}, &mockIdentityStore{}, &config.Config{})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_BucketError(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return false, errors.New("s3 down")
		},
	}
	cfg := &config.Config{
		Buckets: []config.Bucket{{Name: "test"}},
	}
	p := New(s3, &mockIdentityStore{}, cfg)
	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRun_IdentityError(t *testing.T) {
	iam := &mockIdentityStore{
		putIdentityFn: func(ctx context.Context, identity s3config.Identity) error {
			return errors.New("iam down")
		},
	}
	cfg := &config.Config{
		Identities: []config.Identity{{
			Name:        "svc",
			Credentials: []config.Credential{{AccessKey: "key", SecretKey: "secret"}},
			Actions:     []string{"Read"},
		}},
	}
	p := New(&mockS3Admin{}, iam, cfg)
	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from identity provisioning")
	}
}

func TestRun_PolicyError(t *testing.T) {
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return "", nil
		},
		putBucketPolicyFn: func(ctx context.Context, name, policy string) error {
			return errors.New("put failed")
		},
	}
	policyFile := writeTempFile(t, "policy.json", []byte(`{"Version":"2012-10-17"}`))
	cfg := &config.Config{
		Policies: []config.Policy{{Bucket: "docs", File: policyFile}},
	}
	p := New(s3, &mockIdentityStore{}, cfg)
	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from policy provisioning")
	}
}

// --- ensureBucket tests ---

func TestEnsureBucket_Create(t *testing.T) {
	created := false
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return false, nil
		},
		createBucketFn: func(ctx context.Context, name string) error {
			created = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected bucket to be created")
	}
}

func TestEnsureBucket_AlreadyExists(t *testing.T) {
	createCalled := false
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return true, nil
		},
		createBucketFn: func(ctx context.Context, name string) error {
			createCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("CreateBucket should not be called for existing bucket")
	}
}

func TestEnsureBucket_StrategyCreate_Exists(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return true, nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "create")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureBucket_HeadError(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return false, errors.New("head failed")
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureBucket_CreateError(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) {
			return false, nil
		},
		createBucketFn: func(ctx context.Context, name string) error {
			return errors.New("create failed")
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- ensureIdentity tests ---

func TestEnsureIdentity_Success(t *testing.T) {
	var gotIdentity s3config.Identity
	iam := &mockIdentityStore{
		putIdentityFn: func(ctx context.Context, identity s3config.Identity) error {
			gotIdentity = identity
			return nil
		},
	}
	p := New(nil, iam, &config.Config{})

	err := p.ensureIdentity(context.Background(), config.Identity{
		Name:        "svc",
		Credentials: []config.Credential{{AccessKey: "key1", SecretKey: "secret1"}},
		Actions:     []string{"Read", "Write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIdentity.Name != "svc" {
		t.Errorf("expected name 'svc', got %q", gotIdentity.Name)
	}
	if len(gotIdentity.Credentials) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(gotIdentity.Credentials))
	}
	if gotIdentity.Credentials[0].AccessKey != "key1" {
		t.Errorf("expected access key 'key1', got %q", gotIdentity.Credentials[0].AccessKey)
	}
	if len(gotIdentity.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(gotIdentity.Actions))
	}
}

func TestEnsureIdentity_Error(t *testing.T) {
	iam := &mockIdentityStore{
		putIdentityFn: func(ctx context.Context, identity s3config.Identity) error {
			return errors.New("put failed")
		},
	}
	p := New(nil, iam, &config.Config{})

	err := p.ensureIdentity(context.Background(), config.Identity{
		Name:        "svc",
		Credentials: []config.Credential{{AccessKey: "key", SecretKey: "secret"}},
		Actions:     []string{"Read"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- ensurePolicy tests ---

func TestEnsurePolicy_NewPolicy(t *testing.T) {
	policyFile := writeTempFile(t, "policy.json", []byte(`{"Version": "2012-10-17", "Statement": []}`))

	putCalled := false
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return "", nil // no existing policy
		},
		putBucketPolicyFn: func(ctx context.Context, name, policy string) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !putCalled {
		t.Error("expected PutBucketPolicy to be called")
	}
}

func TestEnsurePolicy_UpToDate(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[]}`
	policyFile := writeTempFile(t, "policy.json", []byte(policy))

	putCalled := false
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return policy, nil // same as file
		},
		putBucketPolicyFn: func(ctx context.Context, name, p string) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("PutBucketPolicy should not be called when policy is up to date")
	}
}

func TestEnsurePolicy_WhitespaceDifference(t *testing.T) {
	// File has pretty-printed JSON
	prettyPolicy := `{
  "Version": "2012-10-17",
  "Statement": []
}`
	policyFile := writeTempFile(t, "policy.json", []byte(prettyPolicy))

	// Current policy is compact
	compactPolicy := `{"Version":"2012-10-17","Statement":[]}`

	putCalled := false
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return compactPolicy, nil
		},
		putBucketPolicyFn: func(ctx context.Context, name, p string) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("PutBucketPolicy should not be called when only whitespace differs")
	}
}

func TestEnsurePolicy_Update(t *testing.T) {
	newPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`
	policyFile := writeTempFile(t, "policy.json", []byte(newPolicy))

	oldPolicy := `{"Version":"2012-10-17","Statement":[]}`

	putCalled := false
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return oldPolicy, nil
		},
		putBucketPolicyFn: func(ctx context.Context, name, policy string) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !putCalled {
		t.Error("expected PutBucketPolicy to be called for policy change")
	}
}

func TestEnsurePolicy_FileReadError(t *testing.T) {
	p := New(&mockS3Admin{}, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{
		Bucket: "docs",
		File:   "/nonexistent/policy.json",
	})
	if err == nil {
		t.Fatal("expected error for missing policy file")
	}
}

func TestEnsurePolicy_InvalidJSON(t *testing.T) {
	policyFile := writeTempFile(t, "policy.json", []byte(`not json`))

	p := New(&mockS3Admin{}, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestEnsurePolicy_GetError(t *testing.T) {
	policyFile := writeTempFile(t, "policy.json", []byte(`{"Version":"2012-10-17"}`))

	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return "", errors.New("get failed")
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err == nil {
		t.Fatal("expected error from GetBucketPolicy")
	}
}

func TestEnsurePolicy_PutError(t *testing.T) {
	policyFile := writeTempFile(t, "policy.json", []byte(`{"Version":"2012-10-17"}`))

	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			return "", nil
		},
		putBucketPolicyFn: func(ctx context.Context, name, policy string) error {
			return errors.New("put failed")
		},
	}
	p := New(s3, nil, &config.Config{})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err == nil {
		t.Fatal("expected error from PutBucketPolicy")
	}
}

// --- normalizeJSON tests ---

func TestNormalizeJSON(t *testing.T) {
	compact := normalizeJSON(`{ "key" : "value" , "list" : [ 1 , 2 ] }`)
	expected := `{"key":"value","list":[1,2]}`
	if compact != expected {
		t.Errorf("expected %q, got %q", expected, compact)
	}
}

func TestNormalizeJSON_Invalid(t *testing.T) {
	// Invalid JSON should return the original string
	input := "not json"
	result := normalizeJSON(input)
	if result != input {
		t.Errorf("expected original string for invalid JSON, got %q", result)
	}
}
