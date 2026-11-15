// Package s3config reads and writes SeaweedFS `s3.config` files — the static
// identity document the S3 gateway loads at boot via `-s3.config=<path>`.
//
// SeaweedFS has no runtime API for the identity model this provisioner uses
// (name + credentials + coarse actions). The `weed iam` server speaks the AWS
// IAM query protocol, whose model is users, access keys and policy documents —
// a different shape entirely. Identities are therefore provisioned by writing
// the config file the gateway reads on startup, which means identity changes
// take effect when SeaweedFS boots, not while it is running.
package s3config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// identitiesKey is the top-level field holding the identity list in s3.config.
const identitiesKey = "identities"

// DefaultFileMode is the mode applied to a generated s3.config when none is set.
//
// It is deliberately world-readable. The official SeaweedFS image drops
// privileges to an unprivileged `seaweed` user before starting the gateway, so a
// file written 0600 by the provisioner — which runs as a different user, with a
// uid it cannot predict across containers — is unreadable and SeaweedFS refuses
// to boot. The document holds secret keys in plaintext, so operators who can
// align uids or groups should tighten this via SEAWEEDFS_S3_CONFIG_MODE.
const DefaultFileMode os.FileMode = 0o644

// Credential is an access key pair belonging to an identity.
type Credential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// Identity is a single entry in the s3.config identity list.
type Identity struct {
	Name        string       `json:"name"`
	Credentials []Credential `json:"credentials"`
	Actions     []string     `json:"actions"`
}

// Store reads and writes identities in a SeaweedFS s3.config file.
//
// Writes merge into the existing document rather than replacing it: identities
// absent from the provisioner config are preserved, as are unrelated top-level
// fields such as `accounts`. Without that, generating the file would delete the
// admin identity the provisioner itself authenticates with.
type Store struct {
	path string
	mode os.FileMode
}

// NewStore returns a Store backed by the s3.config file at path, written with
// the given mode. A zero mode selects DefaultFileMode.
func NewStore(path string, mode os.FileMode) *Store {
	if mode == 0 {
		mode = DefaultFileMode
	}
	return &Store{path: path, mode: mode}
}

// Path returns the file this Store reads and writes.
func (s *Store) Path() string {
	return s.path
}

// document loads the raw s3.config JSON object. A missing file yields an empty
// document; every top-level field is retained so unknown keys survive a write.
func (s *Store) document() (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("reading s3 config %q: %w", s.path, err)
	}

	// An existing but empty file is treated as an empty document rather than a
	// parse error, so a pre-created volume mount does not break the first run.
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}

	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing s3 config %q: %w", s.path, err)
	}
	return doc, nil
}

// ListIdentities returns the identities currently present in the file.
func (s *Store) ListIdentities(ctx context.Context) ([]Identity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	doc, err := s.document()
	if err != nil {
		return nil, err
	}
	return identitiesFrom(doc, s.path)
}

// identitiesFrom decodes the identity list out of a raw s3.config document.
func identitiesFrom(doc map[string]json.RawMessage, path string) ([]Identity, error) {
	raw, ok := doc[identitiesKey]
	if !ok {
		return nil, nil
	}

	var identities []Identity
	if err := json.Unmarshal(raw, &identities); err != nil {
		return nil, fmt.Errorf("parsing identities in %q: %w", path, err)
	}
	return identities, nil
}

// PutIdentity merges identity into the file, replacing any entry with the same
// name and appending otherwise. All other identities and fields are preserved.
//
// Within the replaced entry only name, credentials and actions are set; fields
// this package does not model, such as `account`, are kept. A credential whose
// access key is still configured keeps its unknown fields, such as `status`.
func (s *Store) PutIdentity(ctx context.Context, identity Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	doc, err := s.document()
	if err != nil {
		return err
	}

	var entries []object
	if raw, ok := doc[identitiesKey]; ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("parsing identities in %q: %w", s.path, err)
		}
	}

	index := -1
	for i, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err == nil && name == identity.Name {
			index = i
			break
		}
	}

	var existing object
	if index >= 0 {
		existing = entries[index]
	}
	merged, err := mergeIdentity(existing, identity)
	if err != nil {
		return fmt.Errorf("merging identity %q: %w", identity.Name, err)
	}
	if index >= 0 {
		entries[index] = merged
	} else {
		entries = append(entries, merged)
	}

	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encoding identities: %w", err)
	}
	doc[identitiesKey] = encoded

	return s.write(doc)
}

// identityKeyOrder and credentialKeyOrder put the fields SeaweedFS documents
// first when an entry is encoded; any other fields follow in sorted order.
var (
	identityKeyOrder   = []string{"name", "credentials", "actions"}
	credentialKeyOrder = []string{"accessKey", "secretKey"}
)

// mergeIdentity sets the managed fields of identity on a copy of existing (nil
// for a new identity) and returns it.
func mergeIdentity(existing object, identity Identity) (object, error) {
	var oldCredentials []object
	if raw, ok := existing["credentials"]; ok {
		if err := json.Unmarshal(raw, &oldCredentials); err != nil {
			return nil, fmt.Errorf("parsing credentials: %w", err)
		}
	}
	byAccessKey := make(map[string]object, len(oldCredentials))
	for _, c := range oldCredentials {
		var accessKey string
		if err := json.Unmarshal(c["accessKey"], &accessKey); err == nil {
			byAccessKey[accessKey] = c
		}
	}

	credentials := make([]object, len(identity.Credentials))
	for i, c := range identity.Credentials {
		credential := byAccessKey[c.AccessKey].clone()
		if err := credential.set("accessKey", c.AccessKey); err != nil {
			return nil, err
		}
		if err := credential.set("secretKey", c.SecretKey); err != nil {
			return nil, err
		}
		credentials[i] = credential
	}

	encodedCredentials := make([]json.RawMessage, len(credentials))
	for i, c := range credentials {
		encodedCredentials[i] = c.encode(credentialKeyOrder)
	}

	merged := existing.clone()
	if err := merged.set("name", identity.Name); err != nil {
		return nil, err
	}
	if err := merged.set("credentials", encodedCredentials); err != nil {
		return nil, err
	}
	if err := merged.set("actions", identity.Actions); err != nil {
		return nil, err
	}
	return merged, nil
}

// object is a JSON object whose values are kept undecoded, so fields this
// package does not model survive a rewrite unchanged.
type object map[string]json.RawMessage

func (o object) clone() object {
	c := make(object, len(o)+3)
	for k, v := range o {
		c[k] = v
	}
	return c
}

func (o object) set(key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", key, err)
	}
	o[key] = encoded
	return nil
}

// MarshalJSON encodes the object with identity keys first, so an entry the
// provisioner rewrote reads like one written by hand.
func (o object) MarshalJSON() ([]byte, error) {
	return o.encode(identityKeyOrder), nil
}

// encode writes the keys in first, in that order, then the rest sorted.
func (o object) encode(first []string) json.RawMessage {
	if o == nil {
		return json.RawMessage("null")
	}
	keys := make([]string, 0, len(o))
	seen := make(map[string]bool, len(first))
	for _, k := range first {
		if _, ok := o[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(o))
	for k := range o {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, _ := json.Marshal(k) // a string always encodes
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(o[k])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// write renders the document and replaces the file atomically, so a partially
// written config is never visible to a booting SeaweedFS.
func (s *Store) write(doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding s3 config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Best-effort cleanup; on the success path the rename has already consumed it.
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp s3 config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp s3 config: %w", err)
	}
	if err := os.Chmod(tmpName, s.mode); err != nil {
		return fmt.Errorf("setting permissions on temp s3 config: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing s3 config %q: %w", s.path, err)
	}
	return nil
}
