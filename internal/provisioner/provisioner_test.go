package provisioner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"seaweedfs-provisioner/internal/client"
	"seaweedfs-provisioner/internal/config"
	"seaweedfs-provisioner/internal/s3config"
)

// mockS3Admin implements S3Admin for unit tests.
type mockS3Admin struct {
	headBucketFn          func(ctx context.Context, name string) (bool, error)
	createBucketFn        func(ctx context.Context, name string) error
	getBucketPolicyFn     func(ctx context.Context, name string) (string, error)
	putBucketPolicyFn     func(ctx context.Context, name, policy string) error
	getBucketVersioningFn func(ctx context.Context, name string) (string, error)
	putBucketVersioningFn func(ctx context.Context, name string, enabled bool) error
	getBucketLifecycleFn  func(ctx context.Context, name string) ([]client.LifecycleRule, error)
	putBucketLifecycleFn  func(ctx context.Context, name string, rules []client.LifecycleRule) error
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

// The versioning and lifecycle hooks are only reached when a bucket configures
// them, so they fall back to inert defaults rather than forcing every test to
// stub them.
func (m *mockS3Admin) GetBucketVersioning(ctx context.Context, name string) (string, error) {
	if m.getBucketVersioningFn == nil {
		return "", nil
	}
	return m.getBucketVersioningFn(ctx, name)
}

func (m *mockS3Admin) PutBucketVersioning(ctx context.Context, name string, enabled bool) error {
	if m.putBucketVersioningFn == nil {
		return nil
	}
	return m.putBucketVersioningFn(ctx, name, enabled)
}

func (m *mockS3Admin) GetBucketLifecycle(ctx context.Context, name string) ([]client.LifecycleRule, error) {
	if m.getBucketLifecycleFn == nil {
		return nil, nil
	}
	return m.getBucketLifecycleFn(ctx, name)
}

func (m *mockS3Admin) PutBucketLifecycle(ctx context.Context, name string, rules []client.LifecycleRule) error {
	if m.putBucketLifecycleFn == nil {
		return nil
	}
	return m.putBucketLifecycleFn(ctx, name, rules)
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
	p := New(&mockS3Admin{}, &mockIdentityStore{}, &config.Config{}, DefaultOptions())
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
	p := New(s3, &mockIdentityStore{}, cfg, DefaultOptions())
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
	p := New(&mockS3Admin{}, iam, cfg, DefaultOptions())
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
	p := New(s3, &mockIdentityStore{}, cfg, DefaultOptions())
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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(nil, iam, &config.Config{}, DefaultOptions())

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
	p := New(nil, iam, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !putCalled {
		t.Error("expected PutBucketPolicy to be called for policy change")
	}
}

func TestEnsurePolicy_FileReadError(t *testing.T) {
	p := New(&mockS3Admin{}, nil, &config.Config{}, DefaultOptions())

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

	p := New(&mockS3Admin{}, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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
	p := New(s3, nil, &config.Config{}, DefaultOptions())

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

// --- versioning tests ---

// An omitted `versioning` field must not touch the bucket, or the provisioner
// would suspend versioning somebody enabled out of band.
func TestEnsureVersioning_UnsetLeavesBucketAlone(t *testing.T) {
	getCalled, putCalled := false, false
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) {
			getCalled = true
			return "", nil
		},
		putBucketVersioningFn: func(ctx context.Context, name string, enabled bool) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if getCalled || putCalled {
		t.Errorf("versioning must not be touched when unset (get=%v put=%v)", getCalled, putCalled)
	}
}

func TestEnsureVersioning_EnablesWhenDrifted(t *testing.T) {
	var gotEnabled *bool
	s3 := &mockS3Admin{
		headBucketFn:          func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) { return "", nil },
		putBucketVersioningFn: func(ctx context.Context, name string, enabled bool) error {
			gotEnabled = &enabled
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	enabled := true
	err := p.ensureBucket(context.Background(), config.Bucket{Name: "test", Versioning: &enabled}, "update")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEnabled == nil || !*gotEnabled {
		t.Error("expected versioning to be enabled")
	}
}

func TestEnsureVersioning_NoOpWhenAlreadyMatching(t *testing.T) {
	putCalled := false
	s3 := &mockS3Admin{
		headBucketFn:          func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) { return "Enabled", nil },
		putBucketVersioningFn: func(ctx context.Context, name string, enabled bool) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	enabled := true
	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test", Versioning: &enabled}, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("expected no write when versioning already matches")
	}
}

func TestEnsureVersioning_SuspendsWhenFalse(t *testing.T) {
	var gotEnabled *bool
	s3 := &mockS3Admin{
		headBucketFn:          func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) { return "Enabled", nil },
		putBucketVersioningFn: func(ctx context.Context, name string, enabled bool) error {
			gotEnabled = &enabled
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	disabled := false
	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test", Versioning: &disabled}, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEnabled == nil || *gotEnabled {
		t.Error("expected versioning to be suspended")
	}
}

func TestEnsureVersioning_GetErrorPropagates(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) {
			return "", errors.New("get failed")
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	enabled := true
	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test", Versioning: &enabled}, "update"); err == nil {
		t.Fatal("expected error")
	}
}

// --- lifecycle tests ---

func lifecycleBucket(rules ...config.LifecycleRule) config.Bucket {
	return config.Bucket{Name: "test", Lifecycle: rules}
}

func TestEnsureLifecycle_SetsWhenAbsent(t *testing.T) {
	var got []client.LifecycleRule
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return nil, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			got = rules
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	bucket := lifecycleBucket(config.LifecycleRule{ID: "expire-30d", Prefix: "logs/", ExpirationDays: 30})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got))
	}
	if got[0].ID != "expire-30d" || got[0].ExpirationDays != 30 || got[0].Prefix != "logs/" {
		t.Errorf("rule not translated correctly: %+v", got[0])
	}
	if !got[0].Enabled {
		t.Error("rule should default to enabled")
	}
}

func TestEnsureLifecycle_NoOpWhenEqual(t *testing.T) {
	putCalled := false
	existing := []client.LifecycleRule{{ID: "expire-30d", Prefix: "logs/", ExpirationDays: 30, Enabled: true}}
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return existing, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	bucket := lifecycleBucket(config.LifecycleRule{ID: "expire-30d", Prefix: "logs/", ExpirationDays: 30})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("expected no write when lifecycle already matches")
	}
}

// PutBucketLifecycle replaces the whole document, so rules the provisioner does
// not manage have to survive the write.
func TestEnsureLifecycle_PreservesUnmanagedRules(t *testing.T) {
	var got []client.LifecycleRule
	existing := []client.LifecycleRule{{ID: "unmanaged", Prefix: "tmp/", ExpirationDays: 7, Enabled: true}}
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return existing, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			got = rules
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	bucket := lifecycleBucket(config.LifecycleRule{ID: "managed", Prefix: "logs/", ExpirationDays: 30})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the unmanaged rule to survive, got %d rules", len(got))
	}
	if got[0].ID != "unmanaged" {
		t.Errorf("unmanaged rule was displaced: %+v", got)
	}
}

func TestEnsureLifecycle_UpdatesRuleInPlaceByID(t *testing.T) {
	var got []client.LifecycleRule
	existing := []client.LifecycleRule{{ID: "expire", Prefix: "logs/", ExpirationDays: 7, Enabled: true}}
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return existing, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			got = rules
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	bucket := lifecycleBucket(config.LifecycleRule{ID: "expire", Prefix: "logs/", ExpirationDays: 90})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the rule to be replaced, not appended: %d rules", len(got))
	}
	if got[0].ExpirationDays != 90 {
		t.Errorf("expiration = %d, want 90", got[0].ExpirationDays)
	}
}

func TestEnsureLifecycle_DisabledStatus(t *testing.T) {
	var got []client.LifecycleRule
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return nil, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			got = rules
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, DefaultOptions())

	bucket := lifecycleBucket(config.LifecycleRule{ID: "off", ExpirationDays: 30, Status: "disabled"})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Enabled {
		t.Errorf("expected a disabled rule, got %+v", got)
	}
}

func TestLifecycleRulesEqualIgnoresOrder(t *testing.T) {
	a := []client.LifecycleRule{
		{ID: "one", ExpirationDays: 1, Enabled: true},
		{ID: "two", ExpirationDays: 2, Enabled: true},
	}
	b := []client.LifecycleRule{
		{ID: "two", ExpirationDays: 2, Enabled: true},
		{ID: "one", ExpirationDays: 1, Enabled: true},
	}
	if !lifecycleRulesEqual(a, b) {
		t.Error("rule sets differing only in order should compare equal")
	}

	b[0].ExpirationDays = 99
	if lifecycleRulesEqual(a, b) {
		t.Error("rule sets with a changed field should not compare equal")
	}
}

// --- dry-run tests ---

func TestDryRun_SkipsBucketCreate(t *testing.T) {
	created := false
	s3 := &mockS3Admin{
		headBucketFn:   func(ctx context.Context, name string) (bool, error) { return false, nil },
		createBucketFn: func(ctx context.Context, name string) error { created = true; return nil },
	}
	p := New(s3, nil, &config.Config{}, Options{DryRun: true})

	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test"}, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Error("dry run must not create the bucket")
	}
}

func TestDryRun_SkipsVersioningWriteButStillReads(t *testing.T) {
	getCalled, putCalled := false, false
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) {
			getCalled = true
			return "", nil
		},
		putBucketVersioningFn: func(ctx context.Context, name string, enabled bool) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, Options{DryRun: true})

	enabled := true
	if err := p.ensureBucket(context.Background(), config.Bucket{Name: "test", Versioning: &enabled}, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reading is what makes the preview reflect live state.
	if !getCalled {
		t.Error("dry run should still read current versioning")
	}
	if putCalled {
		t.Error("dry run must not write versioning")
	}
}

func TestDryRun_SkipsLifecycleWrite(t *testing.T) {
	putCalled := false
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return true, nil },
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			return nil, nil
		},
		putBucketLifecycleFn: func(ctx context.Context, name string, rules []client.LifecycleRule) error {
			putCalled = true
			return nil
		},
	}
	p := New(s3, nil, &config.Config{}, Options{DryRun: true})

	bucket := lifecycleBucket(config.LifecycleRule{ID: "expire", ExpirationDays: 30})
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("dry run must not write lifecycle")
	}
}

func TestDryRun_SkipsPolicyWriteButStillReads(t *testing.T) {
	getCalled, putCalled := false, false
	s3 := &mockS3Admin{
		getBucketPolicyFn: func(ctx context.Context, name string) (string, error) {
			getCalled = true
			return "", nil
		},
		putBucketPolicyFn: func(ctx context.Context, name, policy string) error {
			putCalled = true
			return nil
		},
	}
	policyFile := writeTempFile(t, "policy.json", []byte(`{"Version":"2012-10-17"}`))
	p := New(s3, nil, &config.Config{}, Options{DryRun: true})

	err := p.ensurePolicy(context.Background(), config.Policy{Bucket: "docs", File: policyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !getCalled {
		t.Error("dry run should still read the current policy")
	}
	if putCalled {
		t.Error("dry run must not write the policy")
	}
}

func TestDryRun_SkipsIdentityWrite(t *testing.T) {
	putCalled := false
	iam := &mockIdentityStore{
		putIdentityFn: func(ctx context.Context, identity s3config.Identity) error {
			putCalled = true
			return nil
		},
	}
	p := New(nil, iam, &config.Config{}, Options{DryRun: true})

	err := p.ensureIdentity(context.Background(), config.Identity{
		Name:        "svc",
		Credentials: []config.Credential{{AccessKey: "key", SecretKey: "secret"}},
		Actions:     []string{"Read"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putCalled {
		t.Error("dry run must not write the identity")
	}
}

// A dry run against a missing bucket cannot read settings, so it must still
// report them rather than erroring on a bucket that does not exist.
func TestDryRun_NewBucketSkipsSettingReads(t *testing.T) {
	s3 := &mockS3Admin{
		headBucketFn: func(ctx context.Context, name string) (bool, error) { return false, nil },
		getBucketVersioningFn: func(ctx context.Context, name string) (string, error) {
			t.Error("must not read versioning for a bucket that does not exist")
			return "", nil
		},
		getBucketLifecycleFn: func(ctx context.Context, name string) ([]client.LifecycleRule, error) {
			t.Error("must not read lifecycle for a bucket that does not exist")
			return nil, nil
		},
	}
	p := New(s3, nil, &config.Config{}, Options{DryRun: true})

	enabled := true
	bucket := config.Bucket{
		Name:       "test",
		Versioning: &enabled,
		Lifecycle:  []config.LifecycleRule{{ID: "expire", ExpirationDays: 30}},
	}
	if err := p.ensureBucket(context.Background(), bucket, "update"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
