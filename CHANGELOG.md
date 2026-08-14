# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Identity provisioning never worked.** The client targeted `GET /iam/api/v1/users`
  on the **filer**, an endpoint that does not exist — the filer answers `404`. SeaweedFS
  serves an IAM API from a separate `weed iam` server (port 8111, started with `-iam`),
  which speaks the AWS IAM query protocol (`POST /` with `Action=ListUsers`) and returns
  `405` for that REST path. Neither `docker-compose.yaml` nor the integration tests ever
  started that server, so no committed configuration could have worked.
- **The whole integration suite hung and failed.** The test helper connected to the filer
  IAM API unconditionally, so every integration test — including bucket-only ones — retried
  the `404` for the full five-minute `totalTimeout` before failing, and leaked its SeaweedFS
  container because the cleanup closure was only returned after the connect calls. Container
  termination is now registered with `t.Cleanup` immediately after creation.

### Changed

- **Identities are now provisioned by generating SeaweedFS's `s3.config` document**
  (`--phase=identities`), the file the S3 gateway loads at boot via `-s3.config`. This
  matches the `name` / `credentials` / `actions` schema the YAML config already used —
  that schema is the static config file's, not the IAM query API's. Identity changes now
  take effect when SeaweedFS boots rather than while it is running.
- **Provisioning split into phases** (`--phase` / `SEAWEEDFS_PHASE`): `identities` writes
  `s3.config` and needs no cluster, `cluster` reconciles buckets and policies against a
  running gateway, `all` (default) runs both. Identities and cluster resources have opposing
  ordering requirements, so a single-pass run cannot satisfy both.
- **`SEAWEEDFS_FILER_URL` replaced by `SEAWEEDFS_S3_CONFIG_PATH`**, which is required when
  identities are configured. The filer is no longer contacted at all.
- `docker-compose.yaml` now runs the two phases as separate services, with SeaweedFS
  depending on the identity phase completing and booting from the generated file. Its
  healthcheck moved from the S3 gateway to the filer, because a gateway with identities
  configured answers unauthenticated probes with `403`.

### Added

- **`--phase` and `--version` CLI flags.** The `--phase` flag takes precedence over
  `SEAWEEDFS_PHASE`, matching the flag/env precedence used by `postgres-provisioner`.
- **`SEAWEEDFS_S3_CONFIG_MODE`** to control the generated file's permissions. The default
  is `0644` rather than `0600` because the official SeaweedFS image drops privileges to an
  unprivileged `seaweed` user before starting the gateway; a `0600` file written by the
  provisioner is unreadable to it and SeaweedFS refuses to boot. The document holds secret
  keys in plaintext, so deployments that can align uids should tighten this.
- **Merging writes.** `s3.config` is read, merged by identity name, and written back, so
  identities the provisioner does not manage survive — including the admin identity it
  authenticates with. Unknown top-level fields such as `accounts` are preserved rather
  than dropped. Writes are atomic (temp file plus rename) so a booting SeaweedFS never
  reads a partial document.
- **Integration coverage for identities**, which previously had none. The test helper now
  boots SeaweedFS from a config produced by the production writer, so a schema regression
  fails the whole suite; a dedicated test runs the full two-phase flow and asserts that a
  provisioned identity can authenticate against the S3 gateway.
- Unit tests for the `s3config` package covering merge, replace-by-name, unknown-field
  preservation, permissions, atomicity and context cancellation.

### Removed

- The filer IAM HTTP client (`client.IAMClient`, `ConnectIAM`, `IAMIdentity`) and its
  309-line test file, which exercised a `httptest` stub of an endpoint that does not exist.

- **`policies/documents-policy.json`**, the example bucket policy the config references. It
  grants a single named principal (`app-service`) read/write on the `documents` bucket rather
  than using `Principal: "*"`. The `policies:` block in `config.example.yaml` is now active;
  it previously had to stay commented out because the file did not exist, which made
  `docker compose up` fail at the policy step.
- **Integration coverage for bucket policies**, which previously had none. The test provisions
  the checked-in example policy against a real cluster and asserts it is stored and that a
  second run is a no-op, so a document SeaweedFS rejects fails CI instead of the quick start.

### Notes

- `config.example.yaml` now declares an `admin` identity. Once any identity is configured
  the S3 gateway enforces authentication, so the cluster phase requires credentials holding
  the `Admin` action.
- The compose healthcheck probes the S3 gateway on the container's own IP. SeaweedFS binds
  its container IP rather than loopback, so the previous `localhost` probe was refused
  outright; the check accepts `403` as "listening" because an authenticated gateway rejects
  unauthenticated probes.
- The cluster-phase service mounts `./policies` at `/policies`. Policy paths resolve against
  the working directory, which is `/` for the scratch-based image.
