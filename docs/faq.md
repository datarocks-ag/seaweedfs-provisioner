# FAQ

After reading this page you can answer the questions that come up once the provisioner runs
for real. The provisioner reads a YAML config and brings a SeaweedFS cluster in line with it:
it writes identities into the `s3.config` file SeaweedFS reads at boot, and creates buckets,
versioning, lifecycle rules and bucket policies over the S3 API. It only adds and changes, it
never deletes. [How it works](concepts.md) explains the run in full, and the
[configuration reference](configuration.md) lists every field.

Log output on this page is trimmed to the relevant fields; real lines also carry `time`.

- [Day-to-day behavior](#day-to-day-behavior)
  - [I removed a bucket or identity from the config. Why does it still exist?](#i-removed-a-bucket-or-identity-from-the-config-why-does-it-still-exist)
  - [How do I turn off a lifecycle rule?](#how-do-i-turn-off-a-lifecycle-rule)
  - [Is it safe to run on every deploy?](#is-it-safe-to-run-on-every-deploy)
  - [Why does every run log "Writing identity"?](#why-does-every-run-log-writing-identity)
  - [When should I use strategy create?](#when-should-i-use-strategy-create)
  - [Does strategy apply to identities and policies?](#does-strategy-apply-to-identities-and-policies)
  - [Why didn't versioning false change anything?](#why-didnt-versioning-false-change-anything)
- [Identities and access](#identities-and-access)
  - [Do I have to restart SeaweedFS after changing an identity?](#do-i-have-to-restart-seaweedfs-after-changing-an-identity)
  - [Why does everything get 403 after I added the first identity?](#why-does-everything-get-403-after-i-added-the-first-identity)
  - [Will the provisioner overwrite my admin identity or accounts?](#will-the-provisioner-overwrite-my-admin-identity-or-accounts)
  - [Is the bucket in a scoped action like Read:documents checked?](#is-the-bucket-in-a-scoped-action-like-readdocuments-checked)
- [Configuration errors](#configuration-errors)
  - [Why do I get "SEAWEEDFS_S3_CONFIG_PATH is required when identities are configured"?](#why-do-i-get-seaweedfs_s3_config_path-is-required-when-identities-are-configured)
  - [I misspelled a key and got no error. Why?](#i-misspelled-a-key-and-got-no-error-why)
  - [Why does the run fail with "environment variable … is not set"?](#why-does-the-run-fail-with-environment-variable--is-not-set)
  - [Can I use ${VAR:-default}?](#can-i-use-var-default)
  - [Can bucket names contain dots?](#can-bucket-names-contain-dots)
  - [Why do I get "no such file or directory" for a policy file in the container?](#why-do-i-get-no-such-file-or-directory-for-a-policy-file-in-the-container)
  - [Can a policy file use ${VAR}?](#can-a-policy-file-use-var)
- [Startup and connection](#startup-and-connection)
  - [Why does the cluster phase wait 5 minutes and then fail with "connection timeout"?](#why-does-the-cluster-phase-wait-5-minutes-and-then-fail-with-connection-timeout)
  - [Why does SeaweedFS exit with "permission denied" on s3.config?](#why-does-seaweedfs-exit-with-permission-denied-on-s3config)
  - [Why does a localhost healthcheck fail, and why does 403 count as healthy?](#why-does-a-localhost-healthcheck-fail-and-why-does-403-count-as-healthy)
  - [Does the identity phase need SeaweedFS running?](#does-the-identity-phase-need-seaweedfs-running)
  - [Does a dry run need the cluster?](#does-a-dry-run-need-the-cluster)
  - [Can I use an HTTPS endpoint with my own certificate authority?](#can-i-use-an-https-endpoint-with-my-own-certificate-authority)
- [Operations](#operations)
  - [What do the exit codes mean?](#what-do-the-exit-codes-mean)
  - [If one bucket fails, are the others still provisioned?](#if-one-bucket-fails-are-the-others-still-provisioned)
  - [Which image tag should I use?](#which-image-tag-should-i-use)

## Day-to-day behavior

### I removed a bucket or identity from the config. Why does it still exist?

Because the provisioner never deletes anything; you remove it yourself.

Removing a line from the config must never be able to delete a bucket full of data or lock a
service out. The same holds for identities, lifecycle rules and bucket policies. To delete a
bucket, first take it out of the config (otherwise the next run creates it again), then
delete it with an identity that has `Admin`:

```console
$ AWS_ACCESS_KEY_ID=<admin-access-key> AWS_SECRET_ACCESS_KEY=<admin-secret-key> \
    aws --endpoint-url http://localhost:8333 s3 rb s3://<bucket>
remove_bucket: <bucket>
```

With `--force`, the AWS CLI also deletes every object in the bucket. This can't be undone:
try it against a throwaway cluster first, such as the repository's
Compose setup, which publishes port 8333.

An identity lives in `s3.config`, so removing it means editing that file and restarting
SeaweedFS; [Change identities on a running cluster](howto/change-identities-on-a-running-cluster.md)
walks through it.

Further reading: [Additive by design](concepts.md#additive-by-design)

### How do I turn off a lifecycle rule?

Set `status: disabled` on the rule; deleting the rule from the config leaves it on the bucket.

The provisioner reads the bucket's current rules, replaces the ones whose `id` matches a
configured rule and keeps the others, so a rule you drop from the YAML stays active:

```yaml
buckets:
  - name: backups
    lifecycle:
      - id: expire-old-snapshots
        prefix: "snapshots/"
        expiration_days: 30
        status: disabled
```

The next run rewrites the lifecycle configuration:

```json
{"level":"INFO","msg":"Setting bucket lifecycle","bucket":"backups","rules":1}
```

and the bucket then reports the rule as `"Status": "Disabled"`. Because the provisioner
rewrites the whole lifecycle document, it carries only `id`, `prefix`, expiration days and
status of every rule across. If someone added rules with other elements out of band, such as
transitions or tag filters, those elements are lost on the rewrite.

Further reading: [Expire objects with lifecycle rules](howto/expire-objects-with-lifecycle-rules.md),
[Lifecycle rules](configuration.md#lifecycle-rules)

### Is it safe to run on every deploy?

Yes, that's what it's built for.

Buckets, versioning, lifecycle rules and policies are read first and only written when they
differ from the config. A second cluster phase on an unchanged config logs no action between
"Provisioning cluster resources" and the final line:

```json
{"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Identities are the exception: they are rewritten on every run, with the same content (see
the next question). Re-running after a failure is equally safe: what succeeded stays, and the
next run continues from there.

Further reading: [What "update" means for each resource](concepts.md#what-update-means-for-each-resource)

### Why does every run log "Writing identity"?

Because the identity phase writes every configured identity on every run, without comparing it
to what is already in `s3.config`.

The write replaces the identity's entry by name, so an unchanged identity is written back
with the same content. Each run logs two lines per identity:

```json
{"level":"INFO","msg":"Writing identity","name":"admin"}
{"level":"INFO","msg":"Identity written","name":"admin","actions":5}
```

A dry run logs "Would write identity" for every identity for the same reason, even when
nothing would change.

Further reading: [Why identities are a file](concepts.md#why-identities-are-a-file)

### When should I use strategy create?

When the provisioner should only create a bucket and then leave its settings to someone else.

With `strategy: create`, an existing bucket is skipped together with its versioning and
lifecycle settings; a bucket that doesn't exist yet is created and gets its settings once.
Set it per bucket or at the top level; a bucket's own value wins. Everywhere else, keep the
default `update`.

```yaml
buckets:
  - name: backups
    strategy: create
    lifecycle:
      - id: expire-old-snapshots
        prefix: "snapshots/"
        expiration_days: 7
```

On an existing bucket the run logs:

```json
{"level":"INFO","msg":"Skipping existing bucket and its settings (strategy=create)","bucket":"backups"}
```

Further reading: [Strategy: update or create](concepts.md#strategy-update-or-create)

### Does strategy apply to identities and policies?

No. Strategy applies to buckets only.

Identities are written on every run, and bucket policies are compared with their file and
written when they differ, whatever `strategy` says. A bucket with `strategy: create` that
also has a `policies` entry still gets its policy updated.

Further reading: [Strategy: update or create](concepts.md#strategy-update-or-create)

### Why didn't versioning false change anything?

Because `versioning: false` only suspends versioning that is currently enabled; on a bucket
that never had versioning, there is nothing to suspend.

The provisioner reads the bucket's versioning status first. It writes only if the status
doesn't match: `true` enables versioning unless it is already `Enabled`; `false` suspends it
only if it is `Enabled`. A bucket where versioning was never configured already counts as
"off". To see the comparison, run with `LOG_LEVEL=debug`:

```json
{"level":"DEBUG","msg":"Bucket versioning up to date","bucket":"documents","enabled":false}
```

If you leave `versioning` out entirely, the provisioner doesn't read or touch the setting at
all, which is the right choice when someone else manages it.

Further reading: [Versioning](configuration.md#versioning)

## Identities and access

### Do I have to restart SeaweedFS after changing an identity?

Yes. SeaweedFS reads `s3.config` only when it starts, and has no API to change these
identities at runtime.

That is why the identity phase runs before SeaweedFS starts. A change to identities on a
running cluster means: run the identity phase, then restart SeaweedFS.
[Change identities on a running cluster](howto/change-identities-on-a-running-cluster.md)
walks through it, and [Rotate an access key without downtime](howto/rotate-an-access-key.md)
covers key rotation.

Further reading: [Why identities are a file](concepts.md#why-identities-are-a-file)

### Why does everything get 403 after I added the first identity?

Because SeaweedFS starts enforcing authentication as soon as `s3.config` holds any identity;
anonymous requests are then refused.

From then on, every client needs the keys of an identity, and the provisioner's cluster phase
needs an identity with `Admin` to list and create buckets. An anonymous request against such a
cluster fails:

```console
$ aws --endpoint-url http://localhost:8333 --no-sign-request s3 ls

aws: [ERROR]: An error occurred (AccessDenied) when calling the ListBuckets operation: Access Denied.
```

Declare an admin identity in the config and pass its keys to the cluster phase as
`SEAWEEDFS_ACCESS_KEY` and `SEAWEEDFS_SECRET_KEY`.

Further reading: [Set up the admin identity the provisioner logs in with](howto/set-up-the-admin-identity.md),
[Step 1: Declare the admin identity](getting-started.md#step-1-declare-the-admin-identity)

### Will the provisioner overwrite my admin identity or accounts?

No. It replaces only the identities in your config, and in them only the fields the config
sets.

Each write reads `s3.config`, finds the entry whose `name` matches a configured identity (or
appends a new one), and writes the file back. Identities that aren't in your config, such as
an admin identity you maintain by hand, stay as they are, and so do other top-level keys such
as `accounts`. In a configured identity, the provisioner sets `name`, `credentials` and
`actions` and keeps every other field, such as `account`. A credential whose access key is
still in the config keeps its other fields, such as `status`; a credential that is no longer
in the config is removed.

Starting from a file with a hand-made `legacy-importer` and a `reports-app` that carry an
`account` and a credential `status`, an identities phase that configures only `reports-app`
with a new action leaves this behind (captured on 2026-11-15; `admin` and `accounts`
omitted):

```json
    {
      "name": "legacy-importer",
      "credentials": [
        {
          "accessKey": "legacy-key",
          "secretKey": "legacy-secret",
          "status": "Active"
        }
      ],
      "actions": [
        "Write:invoices"
      ],
      "account": {
        "id": "finance",
        "displayName": "Finance"
      }
    },
    {
      "name": "reports-app",
      "credentials": [
        {
          "accessKey": "reports-key",
          "secretKey": "reports-secret",
          "status": "Active"
        }
      ],
      "actions": [
        "Read:reports",
        "List:reports"
      ],
      "account": {
        "id": "finance",
        "displayName": "Finance"
      }
    }
```

In the entries it writes, the provisioner puts `name`, `credentials` and `actions` first and
other fields after them, and it sorts the top-level keys alphabetically. Up to 1.0.0, it
dropped every field other than `name`, `credentials` (`accessKey`, `secretKey`) and
`actions` from every identity in the file, including identities it doesn't manage.

Further reading: [Why identities are a file](concepts.md#why-identities-are-a-file),
[Identities](configuration.md#identities)

### Is the bucket in a scoped action like Read:documents checked?

No. Validation checks only the part before the colon; the bucket name after it can be
anything, and the bucket doesn't need to exist.

The prefix must be exactly `Read`, `Write`, `List`, `Tagging`, `Lock` or `Admin`, with that
capitalization. A typo in the bucket name therefore goes unnoticed, and the identity is
denied on the bucket you meant. This config loads and writes an identity scoped to a bucket
that doesn't exist:

```yaml
identities:
  - name: app-service
    credentials:
      - access_key: ${APP_SERVICE_ACCESS_KEY}
        secret_key: ${APP_SERVICE_SECRET_KEY}
    actions: ["Read:no-such-bucket"]
```

A wrong prefix, on the other hand, stops the run:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: identities[0].actions[2]: invalid action \"read:x\" (must start with Read, Write, List, Tagging, Lock, or Admin)"}
```

Further reading: [Actions](configuration.md#actions)

## Configuration errors

### Why do I get "SEAWEEDFS_S3_CONFIG_PATH is required when identities are configured"?

Because the config declares identities and the phase writes them (`identities` or the default
`all`), but `SEAWEEDFS_S3_CONFIG_PATH` is unset or empty.

The provisioner writes identities into the file SeaweedFS loads with `-s3.config`, so it needs
that file's path. Set it to the same path SeaweedFS reads, on a volume both containers share:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"SEAWEEDFS_S3_CONFIG_PATH is required when identities are configured: identities are provisioned by writing the s3.config file SeaweedFS loads via -s3.config"}
```

The cluster phase never reads this variable, so a `--phase=cluster` container doesn't need it.

Further reading: [Environment variables](deployment.md#environment-variables),
[Two phases](concepts.md#two-phases)

### I misspelled a key and got no error. Why?

Because unknown keys in the YAML are ignored; a misspelled key reads as if the field were
missing.

Validation only catches the misspelling when the missing field is required. With
`versionning: true`, the bucket is treated as if `versioning` were omitted, so versioning is
never touched. Run a dry run and check that every change you expect appears. With this
config:

```yaml
buckets:
  - name: documents
    versionning: true   # typo: ignored, versioning stays as it is
```

the dry run against an existing `documents` bucket previews nothing:

```json
{"level":"INFO","msg":"Provisioning cluster resources","buckets":1,"policies":0}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

A misspelled `expiration_days`, by contrast, fails because the field is required:
`buckets[0].lifecycle[0].expiration_days: must be greater than 0`.

Further reading: [Validation](configuration.md#validation),
[Dry-run](concepts.md#dry-run)

### Why does the run fail with "environment variable … is not set"?

Because a `${VAR}` in a field the phase uses refers to a variable the container doesn't
have. The run stops before it changes anything, so `s3.config` and the cluster stay as they
were.

The error names the field and the variable. Here the identities container did not receive
`APP_SERVICE_ACCESS_KEY`:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: expanding environment variables: identities[1].credentials[0].access_key: environment variable \"APP_SERVICE_ACCESS_KEY\" is not set"}
```

Pass the variable to the container that runs this phase. Each phase checks only its own
fields: the identities phase needs the identities' keys, the cluster phase needs only the
variables used in `strategy`, `buckets` and `policies`. A variable that is set but empty is
not reported here; on a required field, validation stops the run with `is required`
instead.

Up to 1.0.0 this was not an error: the text `${APP_SERVICE_ACCESS_KEY}` was written into
`s3.config` as the access key.

Further reading: [Unset variables](configuration.md#unset-variables),
[Keep access keys out of the config file](howto/keep-access-keys-out-of-the-config.md)

### Can I use ${VAR:-default}?

No. Only the plain `${VAR}` form is expanded; anything else inside the braces is treated as
the name of a variable, which doesn't exist.

So the run fails, even when `SEAWEEDFS_ADMIN_ACCESS_KEY` is set:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: expanding environment variables: identities[0].credentials[0].access_key: environment variable \"SEAWEEDFS_ADMIN_ACCESS_KEY:-admin-key\" is not set"}
```

Up to 1.0.0 there was no error, and `${SEAWEEDFS_ADMIN_ACCESS_KEY:-admin-key}` was written
verbatim into `s3.config` as the access key.

Set defaults where you set the environment, for example in your Compose file or Kubernetes
manifest.

Further reading: [Environment variable expansion](configuration.md#environment-variable-expansion)

### Can bucket names contain dots?

No. Bucket names must be 3 to 63 characters of lowercase letters, digits and hyphens, and
start and end with a letter or digit.

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: buckets[0].name: invalid bucket name \"my.bucket\" (must be 3-63 chars, lowercase alphanumeric and hyphens)"}
```

Use a hyphen instead: `my-bucket`.

Further reading: [Buckets](configuration.md#buckets)

### Why do I get "no such file or directory" for a policy file in the container?

Because a relative policy path is resolved from the working directory, which is `/` in the
image, so `policies/documents-policy.json` means `/policies/documents-policy.json` and that
directory isn't mounted.

Mount your policy directory at `/policies`, or use an absolute path that matches your mount.
Without the mount, the cluster phase fails when it reaches the policy:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"provisioning policy for bucket \"documents\": reading policy file \"policies/documents-policy.json\": open policies/documents-policy.json: no such file or directory"}
```

```yaml
  seaweedfs-provisioner:
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./policies:/policies:ro
```

Policy files are read only in the cluster phase, so a missing file doesn't show up in the
identity phase.

Further reading: [Policies](configuration.md#policies),
[Docker Compose](deployment.md#docker-compose)

### Can a policy file use ${VAR}?

No. Only the YAML config is expanded; the policy file is sent to SeaweedFS as it is on disk.

The `file` path in the YAML is expanded, so you can choose a different file per environment
instead:

```yaml
policies:
  - bucket: documents
    file: policies/documents-${ENVIRONMENT}.json
```

Further reading: [Policies](configuration.md#policies),
[Control bucket access with a policy](howto/control-bucket-access-with-a-policy.md)

## Startup and connection

### Why does the cluster phase wait 5 minutes and then fail with "connection timeout"?

Because the provisioner couldn't reach the S3 gateway for the whole retry budget of 5
minutes. The first S3 call is retried while the gateway is unreachable or failing, so that a
cluster that is still starting doesn't fail the run.

Read the `error` field of the first retry warning. A gateway that isn't reachable shows
`connection refused` or a DNS error, here a service name Compose doesn't know:

```json
{"level":"WARN","msg":"SeaweedFS S3 not ready, retrying","attempt":1,"max_retries":15,"delay":1000000000,"error":"operation error S3: ListBuckets, https response error StatusCode: 0, RequestID: , HostID: , request send failed, Get \"http://seaweedfs:8333/?x-id=ListBuckets\": dial tcp: lookup seaweedfs on 127.0.0.11:53: no such host"}
```

Check `SEAWEEDFS_S3_ENDPOINT`, the network, and whether SeaweedFS is running. The `delay`
is in nanoseconds. After the time budget runs out, the run ends with:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"connecting to SeaweedFS S3: connection timeout: context deadline exceeded"}
```

Wrong or missing keys don't cause the wait: the gateway answers with a `403`, and the run
fails at once with `credentials rejected` and the S3 error code, such as
`SignatureDoesNotMatch`. Up to 1.0.0, those were retried too, and a wrong key also ended in
`connection timeout` after 5 minutes.

Further reading: [Connection retry](deployment.md#connection-retry),
[Set up the admin identity the provisioner logs in with](howto/set-up-the-admin-identity.md)

### Why does SeaweedFS exit with "permission denied" on s3.config?

Because the file isn't readable by the user SeaweedFS runs as. The official image drops
privileges to an unprivileged `seaweed` user, and the provisioner writes the file as root.

This happens when you set `SEAWEEDFS_S3_CONFIG_MODE` to `0600` (or similar) without aligning
user or group IDs. SeaweedFS then exits with code 255; its last log lines are:

```text
W1115 16:51:19.331112 auth_credentials.go:663 fail to read /seaweedfs-config/s3.json : open /seaweedfs-config/s3.json: permission denied
F1115 16:51:19.331132 auth_credentials.go:389 fail to load config file /seaweedfs-config/s3.json: fail to read /seaweedfs-config/s3.json : open /seaweedfs-config/s3.json: permission denied
```

The path is the one you pass to SeaweedFS with `-s3.config`.

Remove `SEAWEEDFS_S3_CONFIG_MODE` to get the default `0644`, or tighten the mode only
together with a matching owner or group.

Further reading: [s3.config file permissions](deployment.md#s3config-file-permissions),
[Restrict who can read s3.config](howto/restrict-who-can-read-s3-config.md)

### Why does a localhost healthcheck fail, and why does 403 count as healthy?

Because SeaweedFS listens on its container IP, not on loopback, and a gateway that enforces
authentication answers an anonymous probe with 403.

Inside the SeaweedFS container, a probe against `localhost` is refused, while one against the
container IP gets a 403 (run with `docker compose exec seaweedfs sh`, safe anywhere):

```console
$ wget -q --spider -S http://localhost:8333/
wget: can't connect to remote host: Connection refused
$ wget -q --spider -S http://$(hostname -i):8333/
  HTTP/1.1 403 Forbidden
wget: server returned error: HTTP/1.1 403 Forbidden
```

Any HTTP answer means the gateway is listening, so the healthcheck accepts 200 or 403. In
Compose YAML, write `$` as `$$` so Compose doesn't interpolate it:

```yaml
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider -S http://$$(hostname -i):8333/ 2>&1 | grep -qE 'HTTP/1.[01] (200|403)'"]
```

Further reading: [Docker Compose](deployment.md#docker-compose)

### Does the identity phase need SeaweedFS running?

No. `--phase=identities` only writes `s3.config` and never connects to S3; that is why it
runs before SeaweedFS starts.

It needs the config file, `SEAWEEDFS_S3_CONFIG_PATH` and the environment variables your
identities reference; an unset one stops the run, as described under
[Configuration errors](#configuration-errors). The default phase `all` is different: it connects to S3 first, with the
full retry, and only then writes identities, so with the cluster down it never writes
`s3.config`.

Further reading: [Two phases](concepts.md#two-phases)

### Does a dry run need the cluster?

For the cluster phase, yes: a dry run reads the live state of buckets, versioning, lifecycle
rules and policies, so it connects to S3 with the same retry as a real run.

The identity phase's dry run doesn't need the cluster, but it still needs
`SEAWEEDFS_S3_CONFIG_PATH`. It reports every identity without comparing:

```json
{"level":"INFO","msg":"Dry run: no changes will be applied"}
{"level":"INFO","msg":"Would write identity","name":"admin","credentials":1,"actions":5}
{"level":"INFO","msg":"Would write identity","name":"app-service","credentials":1,"actions":3}
```

A dry run is safe in any environment: only read calls reach SeaweedFS, and nothing is
written to `s3.config`.

Further reading: [Dry-run](concepts.md#dry-run),
[Step 5: Preview before you apply](getting-started.md#step-5-preview-before-you-apply)

### Can I use an HTTPS endpoint with my own certificate authority?

The provisioner has no option for it, but the image honors `SSL_CERT_FILE`, the standard
variable of Go's TLS stack: point it at a mounted CA file.

The image contains the public CA bundle, so an endpoint with a publicly trusted certificate
works with an `https://` URL in `SEAWEEDFS_S3_ENDPOINT`. With a certificate from your own
certificate authority (CA), the connection fails and is retried until the timeout:

```json
{"level":"WARN","msg":"SeaweedFS S3 not ready, retrying","attempt":1,"max_retries":15,"delay":1000000000,"error":"operation error S3: ListBuckets, exceeded maximum number of attempts, 3, https response error StatusCode: 0, RequestID: , HostID: , request send failed, Get \"https://seaweedfs-tls:8333/?x-id=ListBuckets\": tls: failed to verify certificate: x509: certificate signed by unknown authority"}
```

Mount your CA certificate (PEM) and set `SSL_CERT_FILE`:

```yaml
  seaweedfs-provisioner:
    environment:
      SEAWEEDFS_S3_ENDPOINT: https://<seaweedfs-host>:8333
      SSL_CERT_FILE: /certs/ca.pem
    volumes:
      - ./ca.pem:/certs/ca.pem:ro
```

```json
{"level":"INFO","msg":"Connecting to SeaweedFS S3","endpoint":"https://seaweedfs-tls:8333"}
{"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"https://seaweedfs-tls:8333"}
```

The public CAs stay trusted as well: `SSL_CERT_FILE` replaces Go's list of bundle *files*, but
Go still reads every file in the `/etc/ssl/certs` directory, which is where the image keeps its
bundle. Setting `SSL_CERT_DIR` would replace that directory too. This was tested with image
1.0.0; it isn't covered by the project's tests.

Further reading: [Environment variables](deployment.md#environment-variables),
[Go crypto/x509 root certificates](https://pkg.go.dev/crypto/x509#SystemCertPool)

## Operations

### What do the exit codes mean?

`0`: the run succeeded. `1`: something failed, whether phase, config, connection or an S3
call. `2`: an unknown or malformed command-line flag.

The last log line says what failed, as `"msg":"Provisioning failed"` with an `error` field,
or `"Invalid phase"` / `"Invalid dry-run setting"`. Compose's
`service_completed_successfully` and Kubernetes Jobs rely on the exit code. A wrong phase name:

```console
$ docker run --rm ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0 --phase=identity
{"level":"INFO","msg":"Starting seaweedfs-provisioner","version":"1.0.0"}
{"level":"ERROR","msg":"Invalid phase","phase":"identity","valid":["all","identities","cluster"]}
$ echo $?
1
```

Further reading: [Exit codes](deployment.md#exit-codes)

### If one bucket fails, are the others still provisioned?

Only those before it. The run stops at the first error, and the buckets and policies after it
aren't touched.

Buckets are processed in config order, then policies. What succeeded stays, so after fixing
the cause, the next run continues where this one stopped. Here the bucket `reports` was
created, and the run stopped at the first policy, so the second policy was never checked:

```json
{"level":"INFO","msg":"Creating bucket","bucket":"reports"}
{"level":"INFO","msg":"Setting bucket policy","bucket":"invoices","file":"policies/documents-policy.json"}
{"level":"ERROR","msg":"Provisioning failed","error":"provisioning policy for bucket \"invoices\": setting bucket policy: putting bucket policy for \"invoices\": operation error S3: PutBucketPolicy, https response error StatusCode: 400, RequestID: 18E845E74A28E15D7599D4E1, HostID: , api error InvalidPolicyDocument: The content of the policy document is invalid."}
```

The identity phase behaves the same way, and in `all` it runs before the cluster phase.

Further reading: [A run, from start to finish](concepts.md#a-run-from-start-to-finish)

### Which image tag should I use?

A pinned release (`1.0.0`) or minor version (`1.0`).

Per the release workflow, `latest` follows releases and `develop` the development branch;
`sha-<commit>` tags exist as well. There is no major-only tag such as `1`.

```yaml
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
```

Further reading: [Image tags](deployment.md#image-tags)
