package s3config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "s3.config"), 0)
}

// readRaw returns the store file decoded as a generic JSON object.
func readRaw(t *testing.T, s *Store) map[string]json.RawMessage {
	t.Helper()

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("reading store file: %v", err)
	}

	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing store file: %v", err)
	}
	return doc
}

func identity(name, accessKey string, actions ...string) Identity {
	return Identity{
		Name:        name,
		Credentials: []Credential{{AccessKey: accessKey, SecretKey: accessKey + "-secret"}},
		Actions:     actions,
	}
}

func TestListIdentitiesMissingFileReturnsEmpty(t *testing.T) {
	s := newTestStore(t)

	got, err := s.ListIdentities(context.Background())
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no identities, got %d", len(got))
	}
}

// A volume mount may pre-create the file as empty; that must not be a parse error.
func TestListIdentitiesEmptyFileReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIdentities(context.Background())
	if err != nil {
		t.Fatalf("expected no error for empty file, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no identities, got %d", len(got))
	}
}

func TestListIdentitiesMalformedFileErrors(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ListIdentities(context.Background()); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

// The gateway expects lowercase `identities`/`name`/`actions` with camelCase
// `accessKey`/`secretKey`. Getting these wrong silently produces an identity
// SeaweedFS ignores, so assert the wire shape directly.
func TestPutIdentityWritesSeaweedFSSchema(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutIdentity(ctx, identity("app-service", "app-key", "Read", "Write")); err != nil {
		t.Fatalf("put identity: %v", err)
	}

	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`"identities"`, `"name"`, `"actions"`, `"credentials"`,
		`"accessKey"`, `"secretKey"`, `"app-service"`, `"app-key"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rendered config missing %s\ngot:\n%s", want, data)
		}
	}
}

func TestPutIdentityRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := identity("app-service", "app-key", "Read", "Write", "List")
	if err := s.PutIdentity(ctx, want); err != nil {
		t.Fatalf("put identity: %v", err)
	}

	got, err := s.ListIdentities(ctx)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(got))
	}
	if got[0].Name != want.Name {
		t.Errorf("name = %q, want %q", got[0].Name, want.Name)
	}
	if len(got[0].Credentials) != 1 || got[0].Credentials[0].AccessKey != "app-key" {
		t.Errorf("credentials round-tripped wrong: %+v", got[0].Credentials)
	}
	if len(got[0].Actions) != 3 {
		t.Errorf("actions = %v, want 3 entries", got[0].Actions)
	}
}

func TestPutIdentityAppendsWithoutDisturbingOthers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutIdentity(ctx, identity("first", "first-key", "Read")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentity(ctx, identity("second", "second-key", "Write")); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 identities, got %d", len(got))
	}
	if got[0].Name != "first" || got[1].Name != "second" {
		t.Errorf("unexpected identity order: %q, %q", got[0].Name, got[1].Name)
	}
}

func TestPutIdentityReplacesByName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.PutIdentity(ctx, identity("rotating", "old-key", "Read")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdentity(ctx, identity("rotating", "new-key", "Read", "Write")); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the identity to be replaced, got %d entries", len(got))
	}
	if got[0].Credentials[0].AccessKey != "new-key" {
		t.Errorf("access key = %q, want new-key", got[0].Credentials[0].AccessKey)
	}
	if len(got[0].Actions) != 2 {
		t.Errorf("actions = %v, want the updated two-action list", got[0].Actions)
	}
}

// The admin identity the provisioner authenticates with is typically placed in
// s3.config by hand. Overwriting rather than merging would lock the operator out.
func TestPutIdentityPreservesUnmanagedIdentities(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed := `{"identities":[{"name":"admin","credentials":[{"accessKey":"admin-key","secretKey":"admin-secret"}],"actions":["Admin"]}]}`
	if err := os.WriteFile(s.Path(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.PutIdentity(ctx, identity("app-service", "app-key", "Read")); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected admin to survive alongside the new identity, got %d", len(got))
	}
	if got[0].Name != "admin" || got[0].Credentials[0].SecretKey != "admin-secret" {
		t.Errorf("admin identity was mangled: %+v", got[0])
	}
}

// s3.config carries fields this provisioner does not model (e.g. `accounts`).
// A merge that dropped them would silently destroy unrelated configuration.
func TestPutIdentityPreservesUnknownTopLevelFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed := `{"accounts":[{"id":"acct-1","displayName":"Team"}],"identities":[]}`
	if err := os.WriteFile(s.Path(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.PutIdentity(ctx, identity("app-service", "app-key", "Read")); err != nil {
		t.Fatal(err)
	}

	doc := readRaw(t, s)
	accounts, ok := doc["accounts"]
	if !ok {
		t.Fatal("accounts field was dropped during merge")
	}
	if !strings.Contains(string(accounts), "acct-1") {
		t.Errorf("accounts content was altered: %s", accounts)
	}
}

// SeaweedFS reads s3.config as an unprivileged user, so the default must stay
// readable to it — a stricter default makes the gateway refuse to boot.
func TestPutIdentityAppliesDefaultPermissions(t *testing.T) {
	s := newTestStore(t)

	if err := s.PutIdentity(context.Background(), identity("app-service", "app-key", "Read")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != DefaultFileMode {
		t.Errorf("permissions = %04o, want %04o", perm, DefaultFileMode)
	}
}

// Deployments that can align uids should be able to lock the file down, since it
// holds secret keys in plaintext.
func TestPutIdentityHonoursExplicitPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s3.config")
	s := NewStore(path, 0o640)

	if err := s.PutIdentity(context.Background(), identity("app-service", "app-key", "Read")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("permissions = %04o, want 0640", perm)
	}
}

// A booting SeaweedFS must never observe a half-written file, and the temp file
// used to achieve that must not be left behind.
func TestPutIdentityLeavesNoTempFiles(t *testing.T) {
	s := newTestStore(t)

	if err := s.PutIdentity(context.Background(), identity("app-service", "app-key", "Read")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only the config file, found %v", names)
	}
}

func TestPutIdentityCreatesMissingParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "s3.config")
	s := NewStore(path, 0)

	if err := s.PutIdentity(context.Background(), identity("app-service", "app-key", "Read")); err != nil {
		t.Fatalf("expected parent directories to be created: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file was not created: %v", err)
	}
}

func TestOperationsRespectCancelledContext(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.ListIdentities(ctx); err == nil {
		t.Error("ListIdentities should fail on a cancelled context")
	}
	if err := s.PutIdentity(ctx, identity("app-service", "app-key", "Read")); err == nil {
		t.Error("PutIdentity should fail on a cancelled context")
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Error("no file should be written when the context is already cancelled")
	}
}
