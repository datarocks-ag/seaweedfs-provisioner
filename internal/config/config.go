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

// expandEnvVars replaces ${VAR} references with their environment variable values.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})
}

// expandConfig walks the config and expands env vars in string fields.
func expandConfig(cfg *Config) {
	cfg.Strategy = expandEnvVars(cfg.Strategy)

	for i := range cfg.Buckets {
		b := &cfg.Buckets[i]
		b.Name = expandEnvVars(b.Name)
		b.Strategy = expandEnvVars(b.Strategy)
	}

	for i := range cfg.Identities {
		id := &cfg.Identities[i]
		id.Name = expandEnvVars(id.Name)
		for j := range id.Credentials {
			c := &id.Credentials[j]
			c.AccessKey = expandEnvVars(c.AccessKey)
			c.SecretKey = expandEnvVars(c.SecretKey)
		}
		for j := range id.Actions {
			id.Actions[j] = expandEnvVars(id.Actions[j])
		}
	}

	for i := range cfg.Policies {
		p := &cfg.Policies[i]
		p.Bucket = expandEnvVars(p.Bucket)
		p.File = expandEnvVars(p.File)
	}
}

// Load reads and parses a YAML config file, expanding env vars and validating.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	expandConfig(&cfg)

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
