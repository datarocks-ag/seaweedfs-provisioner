# Give an application its own credentials

After this your application has its own access key that can read, write and list one bucket
and nothing else.

## When you need this

An application that stores objects in SeaweedFS needs S3 keys. Handing it the admin keys
lets it read and delete every bucket and create new ones. A separate identity, limited to
the application's bucket, keeps a bug or a leaked key from reaching anything else, and you
can rotate or remove it without touching the admin identity.

## What you need to know first

- **Identity.** A named entry in SeaweedFS's `s3.config` with one or more key pairs and a
  list of actions. The provisioner writes it in the identities phase, and SeaweedFS reads it
  only when it starts. See [Why identities are a file](../concepts.md#why-identities-are-a-file).
- **Scoped actions.** `Read`, `Write` and `List` without a suffix apply to every bucket.
  With a suffix, `Read:<bucket>` applies to that bucket only. The provisioner accepts
  `Read`, `Write`, `List`, `Tagging`, `Lock` and `Admin`, bare or as `<Action>:<anything>`.
  See [Actions](../configuration.md#actions).
- **Admin identity.** The cluster phase logs in with an identity holding `Admin`, which you
  have set up in [Set up the admin identity the provisioner logs in with](set-up-the-admin-identity.md).

The examples use the bucket `reports`, a second bucket `invoices`, the identity
`reports-app`, and the Docker Compose setup from
[Getting started](../getting-started.md#step-4-give-your-application-its-own-identity).
Replace the names with your own. The output was captured against
`chrislusf/seaweedfs:latest` (SeaweedFS 4.53) on 2026-11-15.

## Steps

### 1. Declare the bucket and the identity

Add the application's identity next to the admin identity, and declare its bucket under
`buckets` so the cluster phase creates it:

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

  - name: reports-app
    credentials:
      - access_key: ${REPORTS_ACCESS_KEY}
        secret_key: ${REPORTS_SECRET_KEY}
    actions:
      - "Read:reports"
      - "Write:reports"
      - "List:reports"
```

`Read:reports` lets the application download objects, `Write:reports` upload and delete
them, `List:reports` list them. Leave out what the application doesn't need.

### 2. Pass the application's keys to the identities phase

The identities phase expands `${REPORTS_ACCESS_KEY}` and `${REPORTS_SECRET_KEY}` from its
environment, so add them to the `seaweedfs-identities` service:

```yaml
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    environment:
      # … SEAWEEDFS_CONFIG_PATH, SEAWEEDFS_S3_CONFIG_PATH and the admin keys as before
      REPORTS_ACCESS_KEY: ${REPORTS_ACCESS_KEY}
      REPORTS_SECRET_KEY: ${REPORTS_SECRET_KEY}
```

If one of them is missing, the identities phase fails with
`environment variable "REPORTS_ACCESS_KEY" is not set` and writes nothing; see
[Keep access keys out of the config file](keep-access-keys-out-of-the-config.md).

### 3. Run the identities phase and restart SeaweedFS

On a new setup, `docker compose up` runs the identities phase before SeaweedFS starts. On a
SeaweedFS that is already running, write the identity and restart it, because SeaweedFS
reads `s3.config` only at boot:

```console
$ docker compose run --rm seaweedfs-identities
$ docker compose restart seaweedfs
```

The identities phase writes every identity in the config. Output is trimmed; real lines also
carry `time`:

```json
{"level":"INFO","msg":"Provisioning identities","count":2}
{"level":"INFO","msg":"Writing identity","name":"admin"}
{"level":"INFO","msg":"Identity written","name":"admin","actions":1}
{"level":"INFO","msg":"Writing identity","name":"reports-app"}
{"level":"INFO","msg":"Identity written","name":"reports-app","actions":3}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Until the restart, SeaweedFS rejects the new key with `InvalidAccessKeyId`.
[Change identities on a running cluster](change-identities-on-a-running-cluster.md) covers
this in detail.

### 4. Run the cluster phase

Once SeaweedFS is healthy, the cluster phase creates the buckets:

```console
$ docker compose run --rm seaweedfs-provisioner
```

```json
{"level":"INFO","msg":"Creating bucket","bucket":"reports"}
{"level":"INFO","msg":"Creating bucket","bucket":"invoices"}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

### 5. Give the application the same keys

Pass the values of `REPORTS_ACCESS_KEY` and `REPORTS_SECRET_KEY` to the application,
usually as `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`, with the endpoint
`http://seaweedfs:8333` inside the Compose network. Start it after the cluster phase so its
bucket exists; see
[Step 6 of Getting started](../getting-started.md#step-6-start-the-application-after-the-provisioner).

## Check that it works

Use the AWS CLI with the application's keys. The Compose file publishes port 8333:

```console
$ export AWS_ACCESS_KEY_ID=<reports-access-key> AWS_SECRET_ACCESS_KEY=<reports-secret-key> AWS_DEFAULT_REGION=us-east-1
```

Uploading and listing in `reports` work:

```console
$ aws --endpoint-url http://localhost:8333 s3 cp --no-progress report.txt s3://reports/2026-q3.txt
upload: ./report.txt to s3://reports/2026-q3.txt
$ aws --endpoint-url http://localhost:8333 s3 ls s3://reports/
2026-11-15 16:58:00         18 2026-q3.txt
```

The same operations on `invoices` are refused:

```console
$ aws --endpoint-url http://localhost:8333 s3 cp --no-progress report.txt s3://invoices/2026-q3.txt
upload failed: ./report.txt to s3://invoices/2026-q3.txt An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.
$ aws --endpoint-url http://localhost:8333 s3 ls s3://invoices/

aws: [ERROR]: An error occurred (AccessDenied) when calling the ListObjectsV2 operation: Access Denied.
```

Listing all buckets shows only the one the identity may use:

```console
$ aws --endpoint-url http://localhost:8333 s3 ls
2026-11-15 16:57:13 reports
```

`InvalidAccessKeyId` instead of a result means SeaweedFS hasn't loaded the identity: restart
it. With 1.0.0, also check `s3.config` for a literal `${…}` placeholder.

## Pitfalls

- **The bucket name after the colon is not checked.** With `Write:raports` instead of
  `Write:reports`, the identities phase logs `Identity written` as usual, and after the
  restart the upload fails:

  ```console
  $ aws --endpoint-url http://localhost:8333 s3 cp --no-progress report.txt s3://reports/2026-q4.txt
  upload failed: ./report.txt to s3://reports/2026-q4.txt An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.
  ```

  The provisioner also doesn't check that the bucket is declared under `buckets` or exists.
  Compare the actions with the bucket names when access is denied.
- **Actions are case-sensitive.** `read:reports` fails validation with
  `invalid action "read:reports" (must start with Read, Write, List, Tagging, Lock, or Admin)`.
- **Removing the identity from the YAML doesn't revoke it.** The provisioner never deletes
  identities, so the entry stays in `s3.config`. See
  [Change identities on a running cluster](change-identities-on-a-running-cluster.md).
- **Bucket policies are a separate layer.** An identity's actions and a bucket policy both
  affect access. For per-prefix or per-operation rules, see
  [Control bucket access with a policy](control-bucket-access-with-a-policy.md).

## Further reading

- [Identities](../configuration.md#identities) and [Actions](../configuration.md#actions) in
  the configuration reference.
- [Rotate an access key without downtime](rotate-an-access-key.md).
- [FAQ: Identities and access](../faq.md#identities-and-access).
