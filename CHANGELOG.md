# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
