# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Require Go 1.27.0 (`go.mod`); build image and CI move to Go 1.27.
- Bump `golangci-lint` tool to v2.15.0, required to type-check against Go 1.27.
- Bump dependencies: `go.opentelemetry.io/otel/trace` and `otel/sdk` 1.47.0,
  `otel/exporters/otlp/otlptrace` 1.45.0, `github.com/klauspost/compress`
  1.18.7, `github.com/moby/sys/user` 0.4.1, `github.com/moby/go-archive` 0.3.0
  (#8, #9, #11, #12, #13, #14), plus transitive minimum-version bumps.
- Bump AWS SDK: `aws-sdk-go-v2` 1.48.0, `credentials` 1.21.0, `service/s3`
  1.114.10, `smithy-go` 1.28.9 (#21).
- Bump `testcontainers-go` to 0.45.0 (#22). It replaces
  `github.com/docker/docker`, which has open advisories with no fixed release,
  with the `github.com/moby/moby` modules. Test-only; not in the shipped binary.
- Bump GitHub Actions to their latest majors (#24).
- Split the user documentation out of the README into `docs/`: getting started,
  how it works, configuration reference, deployment (now including Kubernetes,
  exit codes and the real connection-retry behavior), a FAQ, and eight how-to
  guides. The README keeps a quick start and a documentation index.

### Fixed

- Writing an identity to `s3.config` dropped every field the provisioner does not model,
  such as an identity's `account` or a credential's `status`, from every identity in the
  file, including identities absent from the config. Unmanaged identities are now left as
  they are; a configured identity keeps its other fields, and a credential keeps its other
  fields while its access key is still configured.
- The cluster phase retried rejected credentials (`InvalidAccessKeyId`,
  `SignatureDoesNotMatch`, `AccessDenied`) like an unreachable gateway, so a wrong key failed
  only after the five-minute connection budget with `connection timeout`. It now fails at
  once with `connecting to SeaweedFS S3: credentials rejected: …`.
- `SEAWEEDFS_S3_CONFIG_MODE=0` wrote the file with the default `0644` but logged
  `"mode":"0000"`. The log now shows the mode the file gets.
- A `${VAR}` reference to an unset environment variable stayed in the value as literal
  text and passed validation, so a missing key variable wrote the placeholder into
  `s3.config` as a working access or secret key. The run now fails with
  `<path>: environment variable "<VAR>" is not set` before changing anything. Each phase
  checks only the fields it uses, so the cluster phase still runs without the identities'
  key variables. `${VAR:-default}`, which was never supported, now fails the same way
  instead of being written verbatim.
- `policies/documents-policy.json` named its principal as
  `arn:aws:iam:::user/app-service`, which SeaweedFS accepts but never matches, so
  the example policy had no effect. It now uses
  `arn:aws:iam::000000000000:user/app-service`.
- The README's Docker Compose example authenticated the cluster phase with an
  admin key that its own config never declared, so the run failed after five
  minutes of retries. The examples in `docs/` declare the admin identity.
- `config.example.yaml` said `versioning: false` always suspends versioning; it
  only does so when versioning is currently enabled.

## [1.0.0] - 2026-08-14

Initial release.

### Added

- Idempotent provisioning of SeaweedFS S3 buckets, IAM identities and bucket
  policies from a YAML config file. Additive only — buckets, identities and
  policies absent from the config are never deleted.
- **Two provisioning phases** (`--phase` / `SEAWEEDFS_PHASE`): `identities`
  writes the `s3.config` document and needs no running cluster, `cluster`
  reconciles buckets and policies against a running S3 gateway, `all` (default)
  runs both. Identities and cluster resources have opposing ordering
  requirements, so deployments run the identity phase as a pre-boot init
  container and the cluster phase afterwards.
- **Identities are rendered into SeaweedFS's `s3.config` file**, the document
  the S3 gateway loads at boot via `-s3.config`. SeaweedFS has no runtime API
  for this identity model: the `weed iam` server speaks the AWS IAM query
  protocol, whose users / access-keys / policy-documents model is a different
  shape from the `name` / `credentials` / `actions` schema used here. Identity
  changes therefore take effect when SeaweedFS boots, not while it is running.
- **Merging writes to `s3.config`.** The document is read, merged by identity
  name and written back, so identities the provisioner does not manage survive
  — including the admin identity it authenticates with. Unknown top-level
  fields such as `accounts` are preserved rather than dropped, and writes are
  atomic (temp file plus rename) so a booting SeaweedFS never reads a partial
  document.
- **`SEAWEEDFS_S3_CONFIG_PATH`** — target path for the generated identity
  document, required when identities are configured.
- **`SEAWEEDFS_S3_CONFIG_MODE`** — permissions for that file. The default is
  `0644` rather than `0600` because the official SeaweedFS image drops
  privileges to an unprivileged `seaweed` user before starting the gateway; a
  `0600` file written by the provisioner is unreadable to it and SeaweedFS
  refuses to boot. The document holds secret keys in plaintext, so deployments
  that can align uids should tighten this.
- **`buckets[].versioning`** — enables or suspends object versioning. Modelled
  as a tristate: an omitted field leaves the bucket's current setting
  untouched, because a bare `false` would otherwise suspend versioning that was
  enabled out of band.
- **`buckets[].lifecycle`** — object expiration rules with `id`,
  `expiration_days`, and optional `prefix` and `status`. S3 replaces the whole
  lifecycle document on write, so the provisioner reads the current rules and
  merges by `id`, preserving rules it does not manage; an omitted list leaves
  existing rules alone rather than clearing them. Drift comparison is
  order-independent and reads the prefix from either the modern filter or the
  deprecated top-level field, since SeaweedFS returns the legacy shape and
  ignoring it would rewrite the configuration on every run.
- **Bucket policies** loaded from JSON files referenced by path, applied only
  when the stored document drifts from the configured one.
- **Dry-run mode** (`--dry-run` / `DRY_RUN`) — logs every mutation as a preview
  without applying it: no bucket created, no identity written, no policy,
  versioning or lifecycle set. Read-only calls still execute so the preview
  reflects live state. The one gap is a bucket that does not exist yet, whose
  settings cannot be read, so the run reports the intended create and the
  settings that would follow it.
- **CLI flags `--phase`, `--dry-run` and `--version`**, which take precedence
  over their environment variables, matching the flag/env precedence used by
  `postgres-provisioner`.
- `${VAR}` environment variable expansion in string config fields, so secret
  keys never have to be committed to YAML.
- Configurable strategy: `update` (default) or `create` (skip existing), global
  or per-bucket.
- Exponential backoff retry for S3 gateway connectivity (5-minute total
  budget), so the provisioner tolerates a cluster that is still starting.
- Structured JSON logging via `log/slog`, with `LOG_LEVEL` control. Every
  action and every no-op skip is logged.
- Exit `0` on success and `1` on failure, for Kubernetes `Job` compatibility.
- Multi-stage Dockerfile producing a `scratch`-based image, plus a
  `docker-compose.yaml` example running both phases as separate services.
- Integration tests booting SeaweedFS from a config produced by the production
  writer, covering the full two-phase flow, bucket settings and bucket
  policies, so a schema regression fails CI rather than surfacing at deploy
  time.

### Notes

- `config.example.yaml` declares an `admin` identity. Once any identity is
  configured the S3 gateway enforces authentication, so the cluster phase
  requires credentials holding the `Admin` action.
- Policy paths resolve against the process working directory, which is `/` for
  the scratch-based image — hence the compose file mounts `./policies` at
  `/policies`.
- The compose healthcheck probes the S3 gateway on the container's own IP.
  SeaweedFS binds its container IP rather than loopback, so a `localhost` probe
  is refused outright; the check accepts `403` as "listening" because an
  authenticated gateway rejects unauthenticated probes.

[Unreleased]: https://github.com/datarocks-ag/seaweedfs-provisioner/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/datarocks-ag/seaweedfs-provisioner/releases/tag/v1.0.0
