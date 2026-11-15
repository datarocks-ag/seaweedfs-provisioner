package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// validStrategies is the allowlist of update strategy values.
var validStrategies = map[string]bool{
	"":       true, // inherits from parent/default
	"create": true, // only create if missing, skip if exists
	"update": true, // create or update (default behavior)
}

// EffectiveStrategy returns the first non-empty strategy from the given list,
// defaulting to "update" if all are empty.
func EffectiveStrategy(strategies ...string) string {
	for _, s := range strategies {
		if s != "" {
			return s
		}
	}
	return "update"
}

// Config is the top-level YAML configuration.
type Config struct {
	Strategy   string     `yaml:"strategy"`
	Buckets    []Bucket   `yaml:"buckets"`
	Identities []Identity `yaml:"identities"`
	Policies   []Policy   `yaml:"policies"`
}

// Bucket defines an S3 bucket to provision.
type Bucket struct {
	Name     string `yaml:"name"`
	Strategy string `yaml:"strategy"`
	// Versioning is a pointer so that an omitted field leaves the bucket's
	// current setting untouched, rather than reading as an explicit "false"
	// and suspending versioning someone enabled out of band.
	Versioning *bool           `yaml:"versioning"`
	Lifecycle  []LifecycleRule `yaml:"lifecycle"`
}

// LifecycleRule defines an object expiration rule on a bucket.
type LifecycleRule struct {
	ID             string `yaml:"id"`
	Prefix         string `yaml:"prefix"`
	ExpirationDays int    `yaml:"expiration_days"`
	// Status is "enabled" (default) or "disabled".
	Status string `yaml:"status"`
}

// validRuleStatuses is the allowlist of lifecycle rule status values.
var validRuleStatuses = map[string]bool{
	"":         true, // defaults to enabled
	"enabled":  true,
	"disabled": true,
}

// Enabled reports whether the rule should be active.
func (r LifecycleRule) Enabled() bool {
	return r.Status != "disabled"
}

// Credential defines an access key pair for an IAM identity.
type Credential struct {
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

// Identity defines a SeaweedFS IAM identity to provision.
type Identity struct {
	Name        string       `yaml:"name"`
	Credentials []Credential `yaml:"credentials"`
	Actions     []string     `yaml:"actions"`
}

// Policy defines a bucket policy to apply.
type Policy struct {
	Bucket string `yaml:"bucket"`
	File   string `yaml:"file"`
}

// containsNullByte returns true if s contains a null byte (\x00).
func containsNullByte(s string) bool {
	return strings.ContainsRune(s, '\x00')
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)}`)

// UnsetEnvVarError reports a ${VAR} reference to a variable that is not set.
// An empty value counts as set.
type UnsetEnvVarError struct {
	Name string
}

func (e *UnsetEnvVarError) Error() string {
	return fmt.Sprintf("environment variable %q is not set", e.Name)
}

// Sections selects the parts of the config a run uses. A ${VAR} reference to
// an unset variable fails the load only in a selected section; elsewhere it
// stays literal text, so a container that runs one phase needs only the
// variables that phase reads.
type Sections uint8

const (
	// SectionIdentities is identities, used by the identities phase.
	SectionIdentities Sections = 1 << iota
	// SectionCluster is the top-level strategy, buckets and policies, used by
	// the cluster phase.
	SectionCluster

	// SectionAll is every section.
	SectionAll = SectionIdentities | SectionCluster
)

// expandEnvVars replaces ${VAR} references with their environment variable
// values. With strict set, a reference to an unset variable is an error:
// keeping the literal text would pass validation and, in a credential, write
// the placeholder into s3.config as a working key. Without it, the reference
// stays as it is.
func expandEnvVars(s string, strict bool) (string, error) {
	var unset error
	expanded := envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		val, ok := os.LookupEnv(varName)
		if ok {
			return val
		}
		if strict && unset == nil {
			unset = &UnsetEnvVarError{Name: varName}
		}
		return match
	})
	if unset != nil {
		return "", unset
	}
	return expanded, nil
}

// expander expands fields in place and keeps the first error, so expandConfig
// can walk every field without checking an error after each one.
type expander struct {
	strict bool
	err    error
}

func (e *expander) expand(path string, field *string) {
	if e.err != nil {
		return
	}
	expanded, err := expandEnvVars(*field, e.strict)
	if err != nil {
		e.err = fmt.Errorf("%s: %w", path, err)
		return
	}
	*field = expanded
}

// expandConfig walks the config and expands env vars in string fields. Every
// string field must be listed here, or ${VAR} in it is never expanded.
func expandConfig(cfg *Config, sections Sections) error {
	cluster := &expander{strict: sections&SectionCluster != 0}
	cluster.expand("strategy", &cfg.Strategy)

	for i := range cfg.Buckets {
		b := &cfg.Buckets[i]
		prefix := fmt.Sprintf("buckets[%d]", i)
		cluster.expand(prefix+".name", &b.Name)
		cluster.expand(prefix+".strategy", &b.Strategy)
		for j := range b.Lifecycle {
			r := &b.Lifecycle[j]
			rulePrefix := fmt.Sprintf("%s.lifecycle[%d]", prefix, j)
			cluster.expand(rulePrefix+".id", &r.ID)
			cluster.expand(rulePrefix+".prefix", &r.Prefix)
			cluster.expand(rulePrefix+".status", &r.Status)
		}
	}

	for i := range cfg.Policies {
		p := &cfg.Policies[i]
		prefix := fmt.Sprintf("policies[%d]", i)
		cluster.expand(prefix+".bucket", &p.Bucket)
		cluster.expand(prefix+".file", &p.File)
	}

	identities := &expander{strict: sections&SectionIdentities != 0}
	for i := range cfg.Identities {
		id := &cfg.Identities[i]
		prefix := fmt.Sprintf("identities[%d]", i)
		identities.expand(prefix+".name", &id.Name)
		for j := range id.Credentials {
			c := &id.Credentials[j]
			credPrefix := fmt.Sprintf("%s.credentials[%d]", prefix, j)
			identities.expand(credPrefix+".access_key", &c.AccessKey)
			identities.expand(credPrefix+".secret_key", &c.SecretKey)
		}
		for j := range id.Actions {
			identities.expand(fmt.Sprintf("%s.actions[%d]", prefix, j), &id.Actions[j])
		}
	}

	if cluster.err != nil {
		return cluster.err
	}
	return identities.err
}

// Load reads and parses a YAML config file, expanding env vars and validating.
// Every section is used, so any ${VAR} reference to an unset variable fails.
func Load(path string) (*Config, error) {
	return LoadSections(path, SectionAll)
}

// LoadSections is Load for a run that uses only some sections: a ${VAR}
// reference to an unset variable fails only inside those sections.
func LoadSections(path string, sections Sections) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	if err := expandConfig(&cfg, sections); err != nil {
		return nil, fmt.Errorf("expanding environment variables: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// validActionPrefixes defines the allowed action prefixes for IAM identities.
var validActionPrefixes = []string{
	"Read",
	"Write",
	"List",
	"Tagging",
	"Lock",
	"Admin",
}

// bucketNamePattern validates S3-compatible bucket names:
// 3-63 chars, lowercase alphanumeric and hyphens, must start/end with alphanumeric.
var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func validate(cfg *Config) error {
	if err := validateStrategy("strategy", cfg.Strategy); err != nil {
		return err
	}

	if err := validateBuckets(cfg.Buckets); err != nil {
		return err
	}
	if err := validateIdentities(cfg.Identities); err != nil {
		return err
	}
	return validatePolicies(cfg.Policies)
}

// validateStrategy returns an error if the strategy value is invalid.
func validateStrategy(path, value string) error {
	if !validStrategies[value] {
		return fmt.Errorf("%s: invalid strategy %q (must be \"create\" or \"update\")", path, value)
	}
	return nil
}

func validateBuckets(buckets []Bucket) error {
	names := make(map[string]bool)

	for i, b := range buckets {
		prefix := fmt.Sprintf("buckets[%d]", i)

		if err := validateStrategy(prefix+".strategy", b.Strategy); err != nil {
			return err
		}

		if b.Name == "" {
			return fmt.Errorf("%s.name: is required", prefix)
		}
		if containsNullByte(b.Name) {
			return fmt.Errorf("%s.name: contains null byte", prefix)
		}
		if !bucketNamePattern.MatchString(b.Name) {
			return fmt.Errorf("%s.name: invalid bucket name %q (must be 3-63 chars, lowercase alphanumeric and hyphens)", prefix, b.Name)
		}
		if names[b.Name] {
			return fmt.Errorf("%s.name: duplicate bucket name %q", prefix, b.Name)
		}
		names[b.Name] = true

		if err := validateLifecycle(prefix, b.Lifecycle); err != nil {
			return err
		}
	}

	return nil
}

func validateLifecycle(bucketPrefix string, rules []LifecycleRule) error {
	ids := make(map[string]bool)

	for i, r := range rules {
		prefix := fmt.Sprintf("%s.lifecycle[%d]", bucketPrefix, i)

		// S3 keys rules by ID, and the provisioner compares by ID to decide
		// whether a rule has drifted, so it cannot be inferred.
		if r.ID == "" {
			return fmt.Errorf("%s.id: is required", prefix)
		}
		if containsNullByte(r.ID) {
			return fmt.Errorf("%s.id: contains null byte", prefix)
		}
		if ids[r.ID] {
			return fmt.Errorf("%s.id: duplicate lifecycle rule id %q", prefix, r.ID)
		}
		ids[r.ID] = true

		if containsNullByte(r.Prefix) {
			return fmt.Errorf("%s.prefix: contains null byte", prefix)
		}

		if !validRuleStatuses[r.Status] {
			return fmt.Errorf("%s.status: invalid status %q (must be \"enabled\" or \"disabled\")", prefix, r.Status)
		}

		if r.ExpirationDays <= 0 {
			return fmt.Errorf("%s.expiration_days: must be greater than 0", prefix)
		}
	}

	return nil
}

func validateIdentities(identities []Identity) error {
	names := make(map[string]bool)

	for i, id := range identities {
		prefix := fmt.Sprintf("identities[%d]", i)

		if id.Name == "" {
			return fmt.Errorf("%s.name: is required", prefix)
		}
		if containsNullByte(id.Name) {
			return fmt.Errorf("%s.name: contains null byte", prefix)
		}
		if names[id.Name] {
			return fmt.Errorf("%s.name: duplicate identity name %q", prefix, id.Name)
		}
		names[id.Name] = true

		if len(id.Credentials) == 0 {
			return fmt.Errorf("%s.credentials: at least one credential is required", prefix)
		}
		for j, c := range id.Credentials {
			credPrefix := fmt.Sprintf("%s.credentials[%d]", prefix, j)
			if c.AccessKey == "" {
				return fmt.Errorf("%s.access_key: is required", credPrefix)
			}
			if containsNullByte(c.AccessKey) {
				return fmt.Errorf("%s.access_key: contains null byte", credPrefix)
			}
			if c.SecretKey == "" {
				return fmt.Errorf("%s.secret_key: is required", credPrefix)
			}
			if containsNullByte(c.SecretKey) {
				return fmt.Errorf("%s.secret_key: contains null byte", credPrefix)
			}
		}

		if len(id.Actions) == 0 {
			return fmt.Errorf("%s.actions: at least one action is required", prefix)
		}
		for j, action := range id.Actions {
			if !isValidAction(action) {
				return fmt.Errorf("%s.actions[%d]: invalid action %q (must start with Read, Write, List, Tagging, Lock, or Admin)", prefix, j, action)
			}
		}
	}

	return nil
}

// isValidAction checks whether an action string starts with a valid prefix.
// Actions can be bare (e.g. "Read") or scoped (e.g. "Read:bucketname").
func isValidAction(action string) bool {
	for _, p := range validActionPrefixes {
		if action == p || strings.HasPrefix(action, p+":") {
			return true
		}
	}
	return false
}

func validatePolicies(policies []Policy) error {
	buckets := make(map[string]bool)

	for i, p := range policies {
		prefix := fmt.Sprintf("policies[%d]", i)

		if p.Bucket == "" {
			return fmt.Errorf("%s.bucket: is required", prefix)
		}
		if containsNullByte(p.Bucket) {
			return fmt.Errorf("%s.bucket: contains null byte", prefix)
		}
		if buckets[p.Bucket] {
			return fmt.Errorf("%s.bucket: duplicate policy for bucket %q", prefix, p.Bucket)
		}
		buckets[p.Bucket] = true

		if p.File == "" {
			return fmt.Errorf("%s.file: is required", prefix)
		}
		if containsNullByte(p.File) {
			return fmt.Errorf("%s.file: contains null byte", prefix)
		}
	}

	return nil
}
