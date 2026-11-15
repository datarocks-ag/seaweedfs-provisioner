# Rotate an access key without downtime

After this you can replace an identity's key pair, including the admin key the provisioner
logs in with, without a moment in which the client's key is rejected.

## When you need this

A key has leaked, someone with access has left, or your policy requires new keys every few
months. Replacing the key in one step fails: SeaweedFS accepts the new key only after a
restart, and the application only switches when you redeploy it. Whichever happens first,
the application's requests fail until the other catches up. Rotating in two rounds closes
that gap.

## What you need to know first

- **An identity can have several credentials.** Each entry under `credentials` is a key pair
  that SeaweedFS accepts for that identity, with the same actions. See
  [Identities](../configuration.md#identities).
- **The YAML entry replaces the whole entry in `s3.config`.** A credential you remove from
  the YAML disappears from the file on the next identities phase. See
  [What "update" means for each resource](../concepts.md#what-update-means-for-each-resource).
- **SeaweedFS reads `s3.config` only at boot.** Each change takes effect after a SeaweedFS
  restart. [Change identities on a running cluster](change-identities-on-a-running-cluster.md)
  walks through one such change.

Rotation is two rounds of that change, with the client switch in between:

```mermaid
flowchart LR
    A[Old key only] -->|add new key,<br/>identities phase,<br/>restart| B[Old and new key]
    B -->|switch the client| C[Client uses new key]
    C -->|remove old key,<br/>identities phase,<br/>restart| D[New key only]
```

The examples use the identity `reports-app` and the Docker Compose setup from
[Getting started](../getting-started.md#step-4-give-your-application-its-own-identity). The
output was captured against `chrislusf/seaweedfs:latest` (SeaweedFS 4.53) on 2026-11-15.

"Without downtime" refers to the keys. A SeaweedFS restart itself interrupts S3 requests
for a few seconds when you run a single SeaweedFS container.

## Steps

### 1. Add the new key next to the old one

Give the identity a second credential, fed from new environment variables:

```yaml
  - name: reports-app
    credentials:
      - access_key: ${REPORTS_ACCESS_KEY}        # old
        secret_key: ${REPORTS_SECRET_KEY}
      - access_key: ${REPORTS_ACCESS_KEY_2}      # new
        secret_key: ${REPORTS_SECRET_KEY_2}
    actions:
      - "Read:reports"
      - "Write:reports"
      - "List:reports"
```

Add `REPORTS_ACCESS_KEY_2` and `REPORTS_SECRET_KEY_2` to the environment of the
`seaweedfs-identities` service, then write the file and restart SeaweedFS:

```console
$ docker compose run --rm seaweedfs-identities
$ docker compose restart seaweedfs
```

The identities phase logs the identity as written. Output is trimmed; real lines also carry
`time`:

```json
{"level":"INFO","msg":"Writing identity","name":"reports-app"}
{"level":"INFO","msg":"Identity written","name":"reports-app","actions":3}
```

### 2. Check that both keys work

After the restart, SeaweedFS accepts both pairs. With the old pair and with the new pair in
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`:

```console
$ aws --endpoint-url http://localhost:8333 s3 ls s3://reports/
2026-11-15 16:47:11         18 2026-q3.txt
```

The same output with either pair. Don't continue until the new key works: the next step
switches the client to it.

### 3. Switch the client to the new key

Deploy the application with the new pair. Instances that still use the old pair keep
working, so a rolling deployment is fine.

### 4. Remove the old key

Once no client uses the old key, remove it from the YAML:

```yaml
  - name: reports-app
    credentials:
      - access_key: ${REPORTS_ACCESS_KEY_2}
        secret_key: ${REPORTS_SECRET_KEY_2}
    actions:
      - "Read:reports"
      - "Write:reports"
      - "List:reports"
```

Write the file and restart SeaweedFS again:

```console
$ docker compose run --rm seaweedfs-identities
$ docker compose restart seaweedfs
```

Until this restart, the old key still works. In the test, `s3 ls` with the old key
succeeded after the identities phase and before the restart.

You can drop `REPORTS_ACCESS_KEY` and `REPORTS_SECRET_KEY` from the Compose file now. The
next rotation adds a new pair next to the `_2` pair in the same way.

## Check that it works

After the second restart, the old key is rejected:

```console
$ aws --endpoint-url http://localhost:8333 s3 ls s3://reports/

aws: [ERROR]: An error occurred (InvalidAccessKeyId) when calling the ListObjectsV2 operation: The access key ID you provided does not exist in our records.
```

The new key still lists the bucket:

```console
$ aws --endpoint-url http://localhost:8333 s3 ls s3://reports/
2026-11-15 16:47:11         18 2026-q3.txt
```

## Rotate the admin key

The admin key has two consumers that must agree: the YAML, which puts the key into
`s3.config`, and `SEAWEEDFS_ACCESS_KEY` / `SEAWEEDFS_SECRET_KEY`, which the cluster phase
logs in with. If the cluster phase gets a key SeaweedFS doesn't know, it doesn't fail fast:
it retries `InvalidAccessKeyId` for five minutes and then exits with
`connecting to SeaweedFS S3: connection timeout: context deadline exceeded`. See
[Set up the admin identity the provisioner logs in with](set-up-the-admin-identity.md).

Use the same two rounds, and switch the cluster phase only while SeaweedFS knows both keys:

1. Add the new pair as a second credential of `admin`, pass its variables to
   `seaweedfs-identities`, run the identities phase, restart SeaweedFS. Keep the cluster
   phase on the old key.

   ```yaml
     - name: admin
       credentials:
         - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}       # old
           secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
         - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY_2}     # new
           secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY_2}
       actions:
         - "Admin"
   ```

2. Set `SEAWEEDFS_ACCESS_KEY` and `SEAWEEDFS_SECRET_KEY` of the cluster phase to the new
   pair and run it. It connects at the first attempt:

   ```json
   {"level":"INFO","msg":"Connecting to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
   {"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
   {"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
   {"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
   ```

3. Remove the old pair from the YAML, run the identities phase, restart SeaweedFS. A cluster
   phase that still uses the old pair now gets `InvalidAccessKeyId`:

   ```json
   {"level":"WARN","msg":"SeaweedFS S3 not ready, retrying","attempt":1,"max_retries":15,"delay":1000000000,"error":"operation error S3: ListBuckets, https response error StatusCode: 403, RequestID: 18E84656C7C843CDDA6197D0, HostID: , api error InvalidAccessKeyId: The access key ID you provided does not exist in our records."}
   ```

The order matters in step 2. Pointing the cluster phase at the new key before SeaweedFS has
restarted with it, or removing the old key from the YAML while the cluster phase still uses
it, produces the five-minute retry.

## Pitfalls

- **A variable for the new key is missing.** In the identities container, an unset
  `${REPORTS_ACCESS_KEY_2}` stops the identities phase with
  `environment variable "REPORTS_ACCESS_KEY_2" is not set`, and `s3.config` keeps the old
  keys. With 1.0.0 it was written into `s3.config` literally. In the test with 1.0.0 this
  happened to the admin key: the old pair was removed, the new one was the literal text
  `${SEAWEEDFS_ADMIN_ACCESS_KEY_2}`, and the cluster phase was locked out. With 1.0.0, check
  the file before you remove the old key, as shown in
  [Keep access keys out of the config file](keep-access-keys-out-of-the-config.md).
- **Replacing the key in place.** Changing the values behind `${REPORTS_ACCESS_KEY}` instead
  of adding a second credential is a one-round rotation: between the restart and the client
  switch, one side uses a key the other doesn't accept.
- **Forgetting the restart.** The identities phase succeeds without a restart, and nothing
  changes. The old key keeps working, and the new one gets `InvalidAccessKeyId`.

## Further reading

- [Identities](../configuration.md#identities) in the configuration reference.
- [Connection retry](../deployment.md#connection-retry).
- [Change identities on a running cluster](change-identities-on-a-running-cluster.md).
- [FAQ: Identities and access](../faq.md#identities-and-access).
