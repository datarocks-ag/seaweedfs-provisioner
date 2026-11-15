# seaweedfs-provisioner

[![CI](https://github.com/datarocks-ag/seaweedfs-provisioner/actions/workflows/ci.yaml/badge.svg)](https://github.com/datarocks-ag/seaweedfs-provisioner/actions/workflows/ci.yaml)
![coverage](https://raw.githubusercontent.com/datarocks-ag/seaweedfs-provisioner/badges/.badges/develop/coverage.svg)

Declarative, idempotent provisioning for SeaweedFS. Describe the S3 buckets, access-key
identities and bucket policies your services need in one YAML file; the provisioner makes the
cluster match it, every time it runs, and then exits.

It is built to run **next to SeaweedFS's startup**: as Docker Compose services your app
`depends_on`, or as a Kubernetes init container and Job.

## Why

Every service needs the same few things from an S3 store: a bucket, an access key that can
only touch that bucket, perhaps versioning or an expiry rule, and an admin key for everything
else. With SeaweedFS that means hand-editing the `s3.config` JSON file the S3 gateway reads at
boot, and clicking or scripting buckets into place afterwards.

seaweedfs-provisioner replaces that with a file you can review in a pull request:

- **Idempotent:** run it on every deploy. Existing buckets are reconciled, not recreated.
- **Additive:** it never deletes buckets, identities, policies or lifecycle rules that aren't
  in the config.
- **Two phases:** identities are written into `s3.config` *before* SeaweedFS starts; buckets
  and policies are applied over the S3 API *after* it is up.
- **Merges, doesn't overwrite:** identities that aren't in the config stay in `s3.config`,
  including the admin identity you log in with.
- **Previewable:** `--dry-run` logs every change it would make.
- **Small:** a single static binary in a `scratch` image.

## Quick start

Two files next to each other. A config with the admin identity the provisioner logs in with,
and a bucket:

```yaml
# config.yaml
identities:
  - name: admin
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions: ["Admin"]

buckets:
  - name: uploads
    versioning: true
```

And a Compose file that writes `s3.config`, starts SeaweedFS from it, and creates the bucket
once the gateway is healthy:

```yaml
# docker-compose.yaml
services:
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_ADMIN_ACCESS_KEY: admin-key
      SEAWEEDFS_ADMIN_SECRET_KEY: admin-secret
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfs-config:/seaweedfs-config

  seaweedfs:
    image: chrislusf/seaweedfs:latest
    command: server -s3 -filer -s3.config=/seaweedfs-config/s3.json
    depends_on:
      seaweedfs-identities:
        condition: service_completed_successfully
    volumes:
      - seaweedfs-data:/data
      - seaweedfs-config:/seaweedfs-config:ro
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider -S http://$$(hostname -i):8333/ 2>&1 | grep -qE 'HTTP/1.[01] (200|403)'"]
      interval: 5s
      timeout: 5s
      retries: 20

  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=cluster"]
    depends_on:
      seaweedfs:
        condition: service_healthy
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_ENDPOINT: http://seaweedfs:8333
      SEAWEEDFS_ACCESS_KEY: admin-key
      SEAWEEDFS_SECRET_KEY: admin-secret
    volumes:
      - ./config.yaml:/config.yaml:ro

volumes:
  seaweedfs-config:
  seaweedfs-data:
```

```console
$ docker compose up seaweedfs-provisioner
...
seaweedfs-provisioner-1  | {"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
seaweedfs-provisioner-1  | {"level":"INFO","msg":"Creating bucket","bucket":"uploads"}
seaweedfs-provisioner-1  | {"level":"INFO","msg":"Setting bucket versioning","bucket":"uploads","enabled":true}
seaweedfs-provisioner-1  | {"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Run it again and nothing is recreated. The [getting started guide](docs/getting-started.md)
takes this example further: an application identity limited to its own bucket, a dry-run, and
starting your application after the provisioner.

## Documentation

Read in this order if you're new; jump straight to a how-to if you know what you need.

| | |
|---|---|
| **[Getting started](docs/getting-started.md)** | A guided tour: from nothing to SeaweedFS with an admin identity, buckets, and an application that can only reach its own bucket. |
| **[How it works](docs/concepts.md)** | The two phases and why identities are a file, what a run changes and what it never touches, `strategy`, dry-run, logs. |
| **How-to guides** | Task-focused stories: |
| &nbsp;&nbsp;[Set up the admin identity the provisioner logs in with](docs/howto/set-up-the-admin-identity.md) | The identity the cluster phase needs once the gateway enforces authentication. |
| &nbsp;&nbsp;[Give an application its own credentials](docs/howto/give-an-application-its-own-credentials.md) | An access key limited to one bucket. |
| &nbsp;&nbsp;[Change identities on a running cluster](docs/howto/change-identities-on-a-running-cluster.md) | Rewrite `s3.config` and restart SeaweedFS; what survives the rewrite. |
| &nbsp;&nbsp;[Rotate an access key without downtime](docs/howto/rotate-an-access-key.md) | Two keys side by side across two restarts, including the admin key. |
| &nbsp;&nbsp;[Keep access keys out of the config file](docs/howto/keep-access-keys-out-of-the-config.md) | `${VAR}` fed from Docker Compose and Kubernetes secrets, and catching an unset variable. |
| &nbsp;&nbsp;[Expire objects with lifecycle rules](docs/howto/expire-objects-with-lifecycle-rules.md) | Expiry by prefix, changing and disabling rules. |
| &nbsp;&nbsp;[Control bucket access with a policy](docs/howto/control-bucket-access-with-a-policy.md) | A bucket policy file, the principal format SeaweedFS matches, and how it combines with identity actions. |
| &nbsp;&nbsp;[Restrict who can read s3.config](docs/howto/restrict-who-can-read-s3-config.md) | A tighter file mode that SeaweedFS can still read. |
| **[Deployment](docs/deployment.md)** | Image tags, Docker Compose, Kubernetes, binaries, environment variables, CLI flags, retry, exit codes. |
| **[Configuration reference](docs/configuration.md)** | Every field of the YAML file, `${VAR}` expansion and every validation error. |
| **[FAQ](docs/faq.md)** | Questions people ask after their first week. |
| **[Changelog](CHANGELOG.md)** | What changed in each release. |

A complete, commented config lives in [config.example.yaml](config.example.yaml), and
[docker-compose.yaml](docker-compose.yaml) runs it against a local SeaweedFS.

## Development

```bash
make build            # Build binary
make test             # Run unit tests
make test-integration # Run integration tests (requires Docker)
make lint             # Run golangci-lint
make vet              # Run go vet
make fmt              # Format the code
make docker           # Build Docker image
```

The integration tests start `chrislusf/seaweedfs:latest` with testcontainers.

## License

[MIT](LICENSE) — Copyright (c) 2026 Data Rocks AG
