# Set up the admin identity the provisioner logs in with

After this the cluster phase logs in to SeaweedFS with an identity that you declared
yourself, and you can recognize a key mismatch from the first log line instead of after five
minutes.

## When you need this

You declare your first identity, for an application or for yourself, and from then on
SeaweedFS rejects anonymous S3 requests. The provisioner's cluster phase creates buckets and
sets policies over the S3 API, so it now needs keys of its own. This guide sets up that
identity and wires the same key pair into both phases.

## What you need to know first

- **Identities live in `s3.config`.** The identities phase writes them into the JSON file
  that the SeaweedFS S3 gateway loads with `-s3.config` when it starts. SeaweedFS reads the
  file only at boot. See [Why identities are a file](../concepts.md#why-identities-are-a-file)
  and [Two phases](../concepts.md#two-phases).
- **One identity switches authentication on.** That is SeaweedFS behavior: with an empty
  `s3.config` the gateway serves anyone, with one identity in it every request must be
  signed with a known key.
- **The cluster phase logs in with `SEAWEEDFS_ACCESS_KEY` and `SEAWEEDFS_SECRET_KEY`.** If
  either is empty, it connects anonymously. Its first request is `ListBuckets`, followed by
  bucket, versioning, lifecycle and policy calls. An identity with the `Admin` action may do
  all of them. See [Environment variables](../deployment.md#environment-variables) and
  [Actions](../configuration.md#actions).
- **A rejected key fails at once.** The first `ListBuckets` is retried for up to five
  minutes while the gateway is unreachable, but a gateway that rejects the keys fails the
  run on its first answer. See [Connection retry](../deployment.md#connection-retry).

The examples use the Docker Compose setup from
[Getting started](../getting-started.md#step-1-declare-the-admin-identity), with the services
`seaweedfs-identities` (identities phase), `seaweedfs` and `seaweedfs-provisioner` (cluster
phase). The output was captured against `chrislusf/seaweedfs:latest` (SeaweedFS 4.53) on
2026-11-15.

## Steps

### 1. Declare the admin identity in the config

Add an identity with the `Admin` action to `config.yaml`. The keys come from environment
variables, so they stay out of the file:

```yaml
buckets:
  - name: reports
  - name: invoices

identities:
  - name: admin
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions:
      - "Admin"
```

`Admin` alone was enough in the test: the cluster phase connected and created the bucket.
The repository's `config.example.yaml` lists `Read`, `Write`, `List` and `Tagging` as well,
which does no harm.

Declare the admin identity in the YAML even if your current `s3.config` already contains
one. The provisioner leaves identities it does not manage in the file, but a new volume
starts empty, and then only the YAML puts the admin identity back.

### 2. Give both phases the same key pair

The identities phase needs the pair to write it into `s3.config`. The cluster phase needs
the same pair to log in. Feed both from one source, here the Compose variables
`ADMIN_ACCESS_KEY` and `ADMIN_SECRET_KEY` from a `.env` file next to `docker-compose.yaml`:

```yaml
services:
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_ADMIN_ACCESS_KEY: ${ADMIN_ACCESS_KEY}   # written into s3.config
      SEAWEEDFS_ADMIN_SECRET_KEY: ${ADMIN_SECRET_KEY}
    # … volumes as in Getting started

  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=cluster"]
    environment:
      SEAWEEDFS_S3_ENDPOINT: http://seaweedfs:8333
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_ACCESS_KEY: ${ADMIN_ACCESS_KEY}          # used to log in
      SEAWEEDFS_SECRET_KEY: ${ADMIN_SECRET_KEY}
    # … depends_on and volumes as in Getting started
```

```text
# .env — keep out of version control
ADMIN_ACCESS_KEY=<admin-access-key>
ADMIN_SECRET_KEY=<admin-secret-key>
```

Docker Compose substitutes `${ADMIN_ACCESS_KEY}` when it reads the Compose file; the
provisioner then expands `${SEAWEEDFS_ADMIN_ACCESS_KEY}` in `config.yaml` from the
container's environment. With one source for both phases, the two copies of the key cannot
drift apart. More ways to supply the keys are in
[Keep access keys out of the config file](keep-access-keys-out-of-the-config.md).

### 3. Write s3.config, start SeaweedFS, run the cluster phase

```console
$ docker compose up -d seaweedfs
$ docker compose run --rm seaweedfs-provisioner
```

`depends_on` in the Compose file runs the identities phase before SeaweedFS starts. Its log
shows the identity being written. Output is trimmed; real lines also carry `time`:

```json
{"level":"INFO","msg":"Using s3.config for identities","path":"/seaweedfs-config/s3.json","mode":"0644"}
{"level":"INFO","msg":"Provisioning identities","count":1}
{"level":"INFO","msg":"Writing identity","name":"admin"}
{"level":"INFO","msg":"Identity written","name":"admin","actions":1}
```

The cluster phase then logs in at the first attempt:

```json
{"level":"INFO","msg":"Connecting to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
{"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
{"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
{"level":"INFO","msg":"Creating bucket","bucket":"reports"}
{"level":"INFO","msg":"Creating bucket","bucket":"invoices"}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

## Check that it works

`Connected to SeaweedFS S3` directly after `Connecting to SeaweedFS S3`, with no `WARN` line
in between, means the key pair matches. To check the keys from outside, use the AWS CLI. The
Compose file publishes port 8333:

```console
$ AWS_ACCESS_KEY_ID=<admin-access-key> AWS_SECRET_ACCESS_KEY=<admin-secret-key> AWS_DEFAULT_REGION=us-east-1 \
    aws --endpoint-url http://localhost:8333 s3 ls
2026-11-15 16:57:13 invoices
2026-11-15 16:57:13 reports
```

## Pitfalls

- **The keys don't match.** The cluster phase fails on the first answer from SeaweedFS and
  exits with code 1. With a wrong secret key:

  ```json
  {"level":"ERROR","msg":"Provisioning failed","error":"connecting to SeaweedFS S3: credentials rejected: operation error S3: ListBuckets, https response error StatusCode: 403, RequestID: 18E84B03D7A4764C29EFC2E2, HostID: , api error SignatureDoesNotMatch: The request signature we calculated does not match the signature you provided. Check your key and signing method."}
  ```

  The `api error` names the cause:

  | `api error` | Cause |
  |---|---|
  | `SignatureDoesNotMatch` | The access key is known, the secret key is wrong. |
  | `InvalidAccessKeyId` | SeaweedFS doesn't know the access key: a typo, or the identity was written after SeaweedFS started. With 1.0.0 also a literal `${…}` placeholder in `s3.config`. |
  | `AccessDenied` | No keys were sent, because `SEAWEEDFS_ACCESS_KEY` or `SEAWEEDFS_SECRET_KEY` is empty. |

  Up to 1.0.0, the run didn't fail here but retried for five minutes, logging each `403` as
  `SeaweedFS S3 not ready, retrying`, and then ended with `connection timeout`.
- **The cluster phase logs in with an identity that lacks `Admin`.** The connection
  succeeds, because SeaweedFS answers `ListBuckets` for any known identity, and the run
  fails at the first bucket it isn't allowed to see. Here with the keys of an application
  identity limited to `reports`:

  ```json
  {"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
  {"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
  {"level":"ERROR","msg":"Provisioning failed","error":"provisioning bucket \"invoices\": checking bucket: head bucket \"invoices\": operation error S3: HeadBucket, https response error StatusCode: 403, RequestID: 18E846B1759A80E0C2324D55, HostID: , api error Forbidden: Forbidden"}
  ```

- **The identity was written after SeaweedFS started.** A running SeaweedFS answers
  `InvalidAccessKeyId` for a key the identities phase added after SeaweedFS started.
  Restart SeaweedFS; see [Change identities on a running cluster](change-identities-on-a-running-cluster.md).
- **The admin key variable is missing.** If `SEAWEEDFS_ADMIN_ACCESS_KEY` is not set in the
  identities container, the identities phase fails with
  `environment variable "SEAWEEDFS_ADMIN_ACCESS_KEY" is not set` and writes nothing. With
  1.0.0, `${SEAWEEDFS_ADMIN_ACCESS_KEY}` was written into `s3.config` literally instead,
  and the cluster phase got `InvalidAccessKeyId`. See
  [Keep access keys out of the config file](keep-access-keys-out-of-the-config.md).
- **Extra fields on identities with 1.0.0.** Up to 1.0.0, the provisioner rewrote every
  identity with only `name`, `credentials` (`accessKey`, `secretKey`) and `actions`, so a
  field such as `account` on an identity, or `status` on a credential, was gone after the
  next identities phase. Later releases keep those fields; see
  [Will the provisioner overwrite my admin identity or accounts?](../faq.md#will-the-provisioner-overwrite-my-admin-identity-or-accounts)
- **Changing the admin key.** The YAML and `SEAWEEDFS_ACCESS_KEY` / `SEAWEEDFS_SECRET_KEY`
  have to change together, in a fixed order. See
  [Rotate an access key without downtime](rotate-an-access-key.md), section "Rotate the
  admin key".

## Further reading

- [Identities](../configuration.md#identities) and [Actions](../configuration.md#actions) in
  the configuration reference.
- [Connection retry](../deployment.md#connection-retry) and
  [Environment variables](../deployment.md#environment-variables).
- [FAQ: Identities and access](../faq.md#identities-and-access) and
  [FAQ: Startup and connection](../faq.md#startup-and-connection).
- [Give an application its own credentials](give-an-application-its-own-credentials.md).
