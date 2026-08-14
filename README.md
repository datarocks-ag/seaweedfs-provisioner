# seaweedfs-provisioner

[![CI](https://github.com/datarocks-ag/seaweedfs-provisioner/actions/workflows/ci.yaml/badge.svg)](https://github.com/datarocks-ag/seaweedfs-provisioner/actions/workflows/ci.yaml)
![coverage](https://raw.githubusercontent.com/datarocks-ag/seaweedfs-provisioner/badges/.badges/develop/coverage.svg)

A Go CLI tool that idempotently provisions SeaweedFS resources (S3 buckets, IAM identities, bucket policies) from a YAML config file. Designed to run as a one-shot container — either as a Docker Compose init service (via `service_completed_successfully`) or as a Kubernetes `Job` / init container.

## Features

- Idempotent provisioning of S3 buckets, IAM identities, and bucket policies
- Bucket versioning and object lifecycle (expiration) rules
- Dry-run mode that previews every mutation against live state
- YAML config with `${VAR}` environment variable expansion
- Configurable strategy: `update` (default) or `create` (skip existing)
- Identities rendered into the `s3.config` document SeaweedFS loads at boot, merged rather than overwritten
- Bucket policies loaded from JSON files, applied only when they drift
- Exponential backoff retry for the S3 gateway
- Structured JSON logging via `log/slog`
- Never deletes buckets, identities, or policies not in config

## Quick Start

```bash
docker compose up
```

This renders `s3.config`, starts SeaweedFS from it, then provisions buckets against the running cluster.

## Provisioning Phases

Identities and cluster resources have **opposing ordering requirements**, so provisioning splits into two phases:

| Phase | Provisions | Requires | Runs |
|---|---|---|---|
| `identities` | Identities → `s3.config` | Nothing | **Before** SeaweedFS starts |
| `cluster` | Buckets (incl. versioning, lifecycle), policies | A running cluster | **After** SeaweedFS is healthy |
| `all` (default) | Both, in that order | A running cluster | Single-shot |

SeaweedFS has no runtime API for this identity model. The `weed iam` server speaks the AWS IAM query protocol, whose model is users, access keys and policy documents — a different shape from the `name` / `credentials` / `actions` schema used here. Identities are therefore provisioned by writing the config file the S3 gateway reads on startup, which means **identity changes take effect when SeaweedFS boots, not while it is running**.

Use `all` only when SeaweedFS is already up and you intend to restart it afterwards; otherwise run the phases as separate containers.

```bash
seaweedfs-provisioner --phase=identities   # pre-boot
seaweedfs-provisioner --phase=cluster      # post-boot
```

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SEAWEEDFS_S3_ENDPOINT` | no | `http://localhost:8333` | S3 gateway endpoint (cluster phase) |
| `SEAWEEDFS_ACCESS_KEY` | no | — | Admin access key |
| `SEAWEEDFS_SECRET_KEY` | no | — | Admin secret key |
| `SEAWEEDFS_REGION` | no | `us-east-1` | S3 region |
| `SEAWEEDFS_S3_CONFIG_PATH` | conditional | — | Where to write `s3.config`. Required if `identities` are configured |
| `SEAWEEDFS_S3_CONFIG_MODE` | no | `0644` | Octal file mode for the generated `s3.config` |
| `SEAWEEDFS_PHASE` | no | `all` | `identities`, `cluster`, or `all` |
| `DRY_RUN` | no | `false` | Set to `true` to log every mutation as a preview without applying it |
| `SEAWEEDFS_CONFIG_PATH` | no | `./config.yaml` | Path to YAML config |
| `LOG_LEVEL` | no | `info` | Log level (debug/info/warn/error) |

Credentials are optional (a SeaweedFS instance with no identities configured serves the S3 gateway unauthenticated). If either `SEAWEEDFS_ACCESS_KEY` or `SEAWEEDFS_SECRET_KEY` is unset, the S3 client connects anonymously. **Once any identity exists, the gateway enforces auth**, so the cluster phase then needs credentials for an identity holding the `Admin` action.

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--phase` | `all` (or `SEAWEEDFS_PHASE`) | Provisioning phase. The CLI flag takes precedence over the env var |
| `--dry-run` | `false` (or `DRY_RUN`) | Log every mutation as a preview without applying it. Read-only calls still run so the preview reflects live state. The CLI flag takes precedence over the env var |
| `--version` | — | Print version and exit |

## Dry Run

`--dry-run` (or `DRY_RUN=true`) logs what would change without applying anything — no bucket is created, no identity is written to `s3.config`, no policy, versioning or lifecycle is set.

Read-only calls still execute, so the preview reflects live state rather than guessing: existence checks, and current policy, versioning and lifecycle lookups all run. The exception is a bucket that does not exist yet — its settings cannot be read, so the run reports the intended create and the settings that would follow it.

```bash
seaweedfs-provisioner --phase=cluster --dry-run
```

## Strategy

Control whether existing resources are updated or skipped using the `strategy` field:

- `update` (default) — create resources if missing, update if they already exist
- `create` — create resources if missing, skip if they already exist

Strategy can be set globally or per bucket. Per-bucket strategy overrides the global setting.

```yaml
strategy: "create"           # global: skip existing buckets

buckets:
  - name: documents
    strategy: "update"       # override: always reconcile this bucket
```

**Note:** Strategy applies to buckets only. Identities are always written, and bucket policies are always reconciled against the file on disk. `create` on a bucket that already exists also skips its versioning and lifecycle settings, matching how `postgres-provisioner` treats sub-resources.

## Environment Variable Expansion

String values support `${VAR}` syntax. If the variable is set in the environment, it is replaced; if unset, the placeholder is preserved as-is.

```yaml
access_key: "${APP_SERVICE_ACCESS_KEY}"    # replaced with env var value at load time
```

Only the bare `${VAR}` form is recognized. Default-value syntax (`${VAR:-default}`) and dollar-escaping (`$$`) are not supported. Substitution applies to string fields only.

## Buckets

Bucket names must be S3-compatible: 3–63 characters, lowercase alphanumerics and hyphens, starting and ending with an alphanumeric.

```yaml
buckets:
  - name: documents
    versioning: true
  - name: media-assets
  - name: backups
    strategy: "create"
    lifecycle:
      - id: expire-old-snapshots
        prefix: "snapshots/"
        expiration_days: 30
        status: enabled     # enabled (default) | disabled
```

### Versioning

`versioning: true` enables object versioning, `false` suspends it. **Omitting the field leaves the bucket's current setting untouched** — that distinction matters, because a bare `false` would otherwise suspend versioning somebody enabled out of band. The current status is read first, so a bucket already in the desired state is not rewritten.

### Lifecycle

Lifecycle rules expire objects after a number of days, optionally scoped to a key prefix.

| Field | Required | Description |
|---|---|---|
| `id` | yes | Rule identifier. Used to match rules for updates, so it cannot be inferred |
| `expiration_days` | yes | Days after which objects expire; must be > 0 |
| `prefix` | no | Key prefix the rule applies to; empty means the whole bucket |
| `status` | no | `enabled` (default) or `disabled` |

S3 replaces the entire lifecycle document on write, so the provisioner reads the current rules first and **merges by `id`** — rules already on the bucket that are not in the config are preserved, keeping it additive. An empty or omitted `lifecycle` list leaves existing rules alone rather than clearing them.

## Identities

Identities carry one or more access-key credentials and a list of actions. Actions may be bare (cluster-wide) or scoped to a bucket with `Action:bucket`:

| Prefix | Grants |
|---|---|
| `Read` | Read objects |
| `Write` | Write objects |
| `List` | List bucket contents |
| `Tagging` | Read/write object tags |
| `Lock` | Object lock operations |
| `Admin` | Full administrative access |

```yaml
identities:
  - name: app-service
    credentials:
      - access_key: ${APP_SERVICE_ACCESS_KEY}
        secret_key: ${APP_SERVICE_SECRET_KEY}
    actions:
      - "Read"
      - "Write"
      - "List"

  - name: backup-writer
    credentials:
      - access_key: ${BACKUP_ACCESS_KEY}
        secret_key: ${BACKUP_SECRET_KEY}
    actions:
      - "Read:backups"       # scoped to the "backups" bucket
      - "Write:backups"
      - "List:backups"
```

Writes **merge** into any existing `s3.config`: identities absent from the YAML are left alone, as are unrelated top-level fields such as `accounts`. This matters because the admin identity the provisioner authenticates with usually lives in the same file — overwriting rather than merging would lock you out. Replacement is by identity name, so rotating an access key updates in place instead of accumulating stale credentials.

The file is written atomically (temp file plus rename), so a booting SeaweedFS never observes a half-written document.

### File permissions

`s3.config` holds secret keys in plaintext, but the official SeaweedFS image **drops privileges to an unprivileged `seaweed` user** before starting the gateway. A file written `0600` by the provisioner — running as a different user, with a uid it cannot predict across containers — is unreadable, and SeaweedFS refuses to boot:

```
F ... fail to load config file /etc/seaweedfs/s3.json: ... permission denied
```

The default is therefore `0644`. Deployments that can align uids or groups should tighten it:

```yaml
environment:
  SEAWEEDFS_S3_CONFIG_MODE: "0640"
```

## Policies

Bucket policies are standard S3 policy documents, referenced by file path rather than inlined:

```yaml
policies:
  - bucket: documents
    file: policies/documents-policy.json
```

The file must be valid JSON. Paths are resolved relative to the process working directory, not the config file. The image has no `WORKDIR`, so paths resolve against `/` — mount the directory at `/policies` to match the example. The current policy is fetched and compared with whitespace normalized, so reformatting the file alone does not trigger a write.

[policies/documents-policy.json](policies/documents-policy.json) is a worked example: it grants a single named principal read/write on one bucket, rather than `Principal: "*"`. Note that identity `actions` and bucket policies are independent layers — an identity still needs the matching action in `s3.config`.

## Config Example

See [config.example.yaml](config.example.yaml) for a full example.

```yaml
strategy: "update"           # global default: update | create

buckets:
  - name: documents
  - name: backups
    strategy: "create"

identities:
  - name: app-service
    credentials:
      - access_key: ${APP_SERVICE_ACCESS_KEY}
        secret_key: ${APP_SERVICE_SECRET_KEY}
    actions: ["Read", "Write", "List"]

policies:
  - bucket: documents
    file: policies/documents-policy.json
```

## Connection Retry

In the cluster phase the tool retries connecting to the S3 gateway with exponential backoff (1s initial, 30s cap, 15 retries, 5min total timeout). This handles Docker Compose startup ordering without requiring `wait-for-it` scripts. The identity phase talks to nothing and needs no retry.

## Development

```bash
make build            # Build binary
make test             # Run unit tests
make test-integration # Run integration tests (requires Docker)
make lint             # Run golangci-lint
make vet              # Run go vet
make docker           # Build Docker image
```

## Docker Compose Usage

```yaml
services:
  # Phase 1 — before SeaweedFS exists.
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:latest
    command: ["--phase=identities"]
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_ADMIN_ACCESS_KEY: admin-key
      SEAWEEDFS_ADMIN_SECRET_KEY: admin-secret
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfsconfig:/seaweedfs-config

  seaweedfs:
    image: chrislusf/seaweedfs:latest
    command: server -s3 -filer -s3.config=/seaweedfs-config/s3.json
    depends_on:
      seaweedfs-identities:
        condition: service_completed_successfully
    ports: ["8333:8333", "8888:8888"]
    volumes:
      - seaweedfsdata:/data
      - seaweedfsconfig:/seaweedfs-config:ro
    healthcheck:
      # SeaweedFS binds its own container IP, not loopback, so `localhost` is
      # refused. With s3.config in place the gateway answers unauthenticated
      # probes with 403, so treat any HTTP status line as "listening".
      test: ["CMD-SHELL", "wget -q --spider -S http://$$(hostname -i):8333/ 2>&1 | grep -qE 'HTTP/1.[01] (200|403)'"]
      interval: 5s
      timeout: 5s
      retries: 20

  # Phase 2 — against the live cluster.
  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:latest
    command: ["--phase=cluster"]
    depends_on:
      seaweedfs: { condition: service_healthy }
    environment:
      SEAWEEDFS_S3_ENDPOINT: http://seaweedfs:8333
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_ACCESS_KEY: admin-key
      SEAWEEDFS_SECRET_KEY: admin-secret
    volumes:
      - ./config.yaml:/config.yaml:ro
      # Policy paths resolve against the working directory, which is "/" for the
      # scratch-based image, so `policies/x.json` becomes `/policies/x.json`.
      - ./policies:/policies:ro

  app:
    image: your-app
    depends_on:
      seaweedfs-provisioner:
        condition: service_completed_successfully

volumes:
  seaweedfsdata:
  seaweedfsconfig:
```

## Container Image

```bash
docker pull ghcr.io/datarocks-ag/seaweedfs-provisioner:latest
```

## License

[MIT](LICENSE) — Copyright (c) 2026 Data Rocks AG
