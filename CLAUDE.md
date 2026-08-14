# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Goal — SeaweedFS Provisioner

A Go binary (`seaweedfs-provisioner`) that idempotently provisions a SeaweedFS cluster's
application-level state — S3 buckets, IAM identities, and bucket policies — from a YAML config
file. Runs as a one-shot Kubernetes `Job` / init container or Docker Compose init service, then
exits. No long-running process, no health endpoint.

See [README.md](README.md) for the user-facing contract: phases, environment variables, YAML
schema, and Docker Compose usage. This file covers what the README does not — the internal
structure and the constraints that are easy to break.

## Build & Test Commands

```sh
make build             # go build -o seaweedfs-provisioner ./cmd/seaweedfs-provisioner
make test              # go test -race ./...
make test-integration  # go test -race -tags=integration -v ./...   (requires Docker)
make lint              # go tool golangci-lint run
make vet               # go vet ./...
make fmt               # go fmt ./...
make mod-tidy          # go mod tidy
make docker            # docker build -t seaweedfs-provisioner .
make clean

go test -race ./internal/s3config/                            # single package
go test -race -run TestEnsureBucket ./internal/provisioner/   # single test
```

- Go 1.25+, `CGO_ENABLED=0` (pure Go, no C dependencies)
- golangci-lint is a `tool` dependency in `go.mod` — always `go tool golangci-lint run`, never a
  globally installed binary
- Integration tests use `testcontainers-go` with `chrislusf/seaweedfs:latest` and the
  `//go:build integration` tag — plain `go test ./...` skips them
- **CI lints without the integration tag**, so `go tool golangci-lint run` alone will not check
  `integration_test.go`. Use `--build-tags=integration` when touching it
- Coverage gate: 60% total (`.testcoverage.yml`), enforced in CI

## Architecture

```
seaweedfs-provisioner/
├── cmd/seaweedfs-provisioner/
│   └── main.go              # entry point, flag/env parsing, phase dispatch, exit codes
├── internal/
│   ├── config/              # YAML load, ${VAR} expansion, validation
│   ├── client/              # S3 client (AWS SDK) for buckets and policies
│   ├── s3config/            # reads/writes the SeaweedFS s3.config identity document
│   └── provisioner/         # idempotent reconciliation, one file per resource kind
│       ├── provisioner.go   # S3Admin / IdentityStore interfaces, phase methods
│       ├── buckets.go
│       ├── identities.go
│       └── policies.go
├── Dockerfile
├── Makefile
├── go.mod / go.sum
└── config.example.yaml
```

### Two phases with opposing ordering requirements

This is the central design constraint. Identities are written into the `s3.config` file the S3
gateway reads **at boot**, so they must be provisioned *before* SeaweedFS starts. Buckets and
policies go over the S3 API and need it *running*. `Provisioner.ProvisionIdentities` and
`ProvisionCluster` exist so a deployment can run one pass as a pre-boot init container and
another afterwards; `Run` does both and is only correct when the cluster is already up and will
be restarted.

`main.go` dispatches on `--phase` / `SEAWEEDFS_PHASE` and only constructs the dependency each
phase needs — the identity phase never connects to S3. `New` accepts nil for either dependency;
`main.go` declares the S3 client as the `provisioner.S3Admin` interface rather than
`*client.S3Client` so skipping the cluster phase yields a nil interface, not a non-nil interface
wrapping a nil pointer.

### Why identities are a file and not an API call

SeaweedFS has no runtime API for this identity model. Verified against `chrislusf/seaweedfs:latest`:

- `GET /iam/api/v1/users` on the **filer** (`:8888`) → `404`. The endpoint does not exist.
- The IAM API is a **separate server** — `weed iam`, `:8111`, started with `-iam` — speaking the
  AWS IAM query protocol: `POST /` with `Action=ListUsers&Version=2010-05-08`, XML responses.
  That path returns `405` even there.
- Its model is users + access keys + policy documents, not `name` / `credentials` / `actions`.

The YAML schema in this repo *is* the static `s3.config` schema, which is why file generation is
the right mechanism. Earlier versions targeted the non-existent filer REST endpoint; identity
provisioning never worked. Do not "restore" it.

### s3.config writes merge, and that is load-bearing

`s3config.Store.PutIdentity` reads the document, replaces by identity name, and writes back:

- Identities absent from the YAML survive — including the **admin identity the provisioner
  itself authenticates with**, which usually lives in the same file. An overwrite locks the
  operator out of their own cluster.
- Unknown top-level fields (e.g. `accounts`) are preserved by decoding into
  `map[string]json.RawMessage` and only touching `identities`. Decoding into a typed struct
  would silently drop them.
- Writes are atomic (temp file + rename) so a booting SeaweedFS never reads a partial document.

### File permissions are a real constraint, not a detail

The default mode is `0644`, overridable via `SEAWEEDFS_S3_CONFIG_MODE`. The file holds secret
keys in plaintext, so `0600` looks correct — but the official image's entrypoint **drops
privileges to an unprivileged `seaweed` user** before starting the gateway. A `0600` file written
by the provisioner (a different user, with a uid it cannot predict across containers) is
unreadable and SeaweedFS exits 255 with `fail to load config file ... permission denied`. This
was caught by the integration tests; do not tighten the default without re-running them.

### Tristate and merge semantics in bucket settings

Two spots where the obvious implementation is wrong:

- `Bucket.Versioning` is a `*bool`, not a `bool`. An omitted field must leave the bucket alone; a
  plain `bool` would read as `false` and suspend versioning enabled out of band.
- `PutBucketLifecycle` replaces the **entire** lifecycle document, so `ensureLifecycle` reads the
  current rules and merges by rule ID before writing. Writing only the configured rules would
  delete every rule the provisioner does not manage. Same hazard as the s3.config merge.

Drift comparison (`lifecycleRulesEqual`) is order-independent, and `lifecyclePrefix` reads the
prefix from either the modern `Filter` or the deprecated top-level `Prefix`, because SeaweedFS
returns the legacy shape — ignoring it would make every run see drift and rewrite.

### Dry-run

`Options.DryRun` follows `postgres-provisioner`: read-only calls still execute so the preview
reflects live state, only mutations are skipped. The exception is a bucket that does not exist
yet — its settings cannot be read, so `previewSettingsForNewBucket` reports intent instead. When
adding a mutation, guard it with `p.opts.DryRun` and log a "Would ..." line.

### Config expansion is field-by-field

`config.expandEnvVars` uses a custom regexp, **not** `os.ExpandEnv` (unlike the siblings), and
`expandConfig` walks every string field explicitly. **Adding a new string field to a config struct
requires adding it to `expandConfig` too**, or values in that field will never expand.

Policy `file:` paths are resolved relative to the process working directory, not the config file.

### Logging

- `log/slog` with `slog.NewJSONHandler(os.Stdout, ...)`, key-value pairs — never `fmt.Print`
- `LOG_LEVEL` env var controls level (debug/info/warn/error, default: info)
- Log every action taken and every no-op skip

### Retry / Backoff (ecosystem standard)

```go
maxRetries   = 15
initialDelay = 1 * time.Second
maxDelay     = 30 * time.Second
totalTimeout = 5 * time.Minute
```

Exponential backoff: `math.Pow(2, attempt)` seconds, capped at `maxDelay`, in `ConnectS3`. Note
the failure mode this creates in tests: an authorization error looks identical to an outage for
five minutes. Integration tests that check credentials build an `s3.Client` directly instead
(`s3ClientAs`).

### Exit Codes

- `0` — provisioning completed (k8s Job compatible)
- `1` — invalid phase, config load, connection, or provisioning failure

## Key Conventions

- **No shared libraries** across provisioners — each project is standalone
- `gopkg.in/yaml.v3` for config parsing
- Dockerfile: multi-stage `golang:1.25-alpine` builder → `scratch` final image, CA certs copied in
- `-ldflags="-s -w -X main.version=${VERSION}"` for minimal binary + version injection
- Errors always wrapped: `fmt.Errorf("<lowercase gerund phrase>: %w", err)`
- Validation errors are path-prefixed — `identities[0].credentials[1].secret_key: is required`
- Additive only: never delete buckets, identities, or policies absent from the config
- SeaweedFS error classification is string matching (`isNotFoundError`, `isNoSuchPolicyError`)
  because the S3 gateway does not return typed AWS errors reliably. Extend those helpers rather
  than reaching for `errors.As` on AWS error types
- Integration tests boot SeaweedFS from a config produced by the **production** writer, so a
  schema regression fails the whole suite instead of surfacing at deploy time. Keep it that way
- Keep `README.md` and `CHANGELOG.md` current — every provisioner in this ecosystem ships both,
  and the changelog follows Keep a Changelog + SemVer with an `## [Unreleased]` section at the top

## CI / Release

- `.github/workflows/ci.yaml` — runs on `feature/**`, `bugfix/**`, `hotfix/**`, `release/**`,
  `dependabot/**`: lint, unit tests + coverage badge, integration tests, Trivy scan, build
- `.github/workflows/release.yaml` — runs on `develop` and `v*` tags: publishes a multi-arch image
  to `ghcr.io/datarocks-ag/seaweedfs-provisioner`; GoReleaser runs on tags only
- There is no plain-push CI on `develop`/`main` — work lands on prefixed branches via PR
- `chrislusf/seaweedfs:latest` is unpinned in tests and compose, so upstream changes can break CI
  without any local change

## Project-Specific Notes

- Strategy applies to **buckets only** — identities are always written, policies are always
  reconciled against the file. This mirrors `nats-provisioner`, where strategy applies to streams
  but not consumers.
- **Policy paths resolve against the working directory**, which is `/` for the scratch-based
  image (no `WORKDIR`). `policies/x.json` in the config therefore means `/policies/x.json` in the
  container, which is why compose mounts `./policies:/policies:ro`. `TestIntegrationBucketPolicy`
  uses `t.Chdir` to the repo root for the same reason.
- **SeaweedFS binds its container IP, not loopback.** A `localhost` healthcheck inside the
  container is refused outright, and an authenticated gateway answers unauthenticated probes with
  `403`. The compose healthcheck therefore probes `$(hostname -i):8333` and treats any HTTP status
  line as success. In compose YAML the `$` must be written `$$` to survive interpolation.
