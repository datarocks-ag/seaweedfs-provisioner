# Expire objects with lifecycle rules

After this you can declare, change and switch off expiration rules for a bucket in the
config file, and confirm on the bucket what SeaweedFS actually stores.

## When you need this

A bucket collects objects that lose their value after a while: nightly snapshots, temporary
exports, upload leftovers. Without a rule they pile up until someone deletes them by hand.
A lifecycle rule tells SeaweedFS to expire objects under a key prefix after a number of days,
and declaring it in the config means every environment gets the same rule.

## What you need to know first

- **Lifecycle rule.** An S3 lifecycle rule has an ID, a status, an optional key prefix and an
  action. The provisioner manages one kind of action: expiration after `expiration_days`
  days. See [Lifecycle rules](../configuration.md#lifecycle-rules).
- **Merge by ID.** S3 replaces the whole lifecycle document on every write, so the provisioner
  reads the current rules, replaces or appends the configured ones by `id`, and writes the
  result back. Rules on the bucket that the config doesn't list are kept. See
  [What "update" means for each resource](../concepts.md#what-update-means-for-each-resource).
- **Cluster phase.** Lifecycle rules go over the S3 API, so they are applied by the `cluster`
  phase against a running SeaweedFS. See [Two phases](../concepts.md#two-phases).

The examples use the bucket `backups`, the rule ID `expire-old-snapshots` and the Docker
Compose setup from [Getting started](../getting-started.md#step-3-create-a-bucket). Replace
them with your own names.

## Steps

### 1. Add the rule to the bucket

Add a `lifecycle` list to the bucket. Each rule needs a unique `id` and an
`expiration_days` greater than 0; `prefix` limits the rule to keys that start with it, and
`status` is `enabled` (the default) or `disabled`:

```yaml
buckets:
  - name: backups
    lifecycle:
      - id: expire-old-snapshots
        prefix: "snapshots/"
        expiration_days: 30
        status: enabled

identities:
  - name: admin                       # the cluster phase logs in with this identity
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions: ["Admin", "Read", "Write", "List", "Tagging"]
```

Choose the `id` with care: it is how the provisioner recognizes the rule on later runs. If
you rename it, the next run adds a second rule and leaves the old one in place.

### 2. Run the cluster phase

Run the provisioner against the running cluster. With the Compose file from Getting started,
that is the `seaweedfs-provisioner` service, which runs `--phase=cluster`:

```console
$ docker compose run --rm seaweedfs-provisioner
```

On the first run the provisioner creates the bucket and then writes the rule. Output is
trimmed; real lines also carry `time`:

```json
{"level":"INFO","msg":"Creating bucket","bucket":"backups"}
{"level":"INFO","msg":"Setting bucket lifecycle","bucket":"backups","rules":1}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

`rules` is the number of rules in the document the provisioner wrote, including rules it
doesn't manage.

### 3. Change the rule

To keep snapshots for 14 days instead of 30, change `expiration_days` and run the cluster
phase again. Use `LOG_LEVEL=debug` to also see the checks that find nothing to do:

```console
$ docker compose run --rm -e LOG_LEVEL=debug seaweedfs-provisioner
```

```json
{"level":"DEBUG","msg":"Bucket already exists","bucket":"backups"}
{"level":"INFO","msg":"Setting bucket lifecycle","bucket":"backups","rules":1}
```

Run it once more without changing anything, and the provisioner leaves the bucket alone:

```json
{"level":"DEBUG","msg":"Bucket already exists","bucket":"backups"}
{"level":"DEBUG","msg":"Bucket lifecycle up to date","bucket":"backups","rules":1}
```

The comparison ignores the order of the rules, so a rule set that SeaweedFS returns in a
different order doesn't count as a change.

### 4. Switch a rule off

The provisioner never deletes a rule; it is additive by design (see
[Additive by design](../concepts.md#additive-by-design)). Removing the rule from the config
only means the provisioner stops managing it: the rule stays on the bucket, with the status
it last had.

To stop a rule, set its status to `disabled` and run the cluster phase:

```yaml
    lifecycle:
      - id: expire-old-snapshots
        prefix: "snapshots/"
        expiration_days: 14
        status: disabled
```

```json
{"level":"INFO","msg":"Setting bucket lifecycle","bucket":"backups","rules":1}
```

To delete rules for good, use the S3 API yourself, with the admin identity's keys:

- `aws s3api delete-bucket-lifecycle --bucket backups` removes **every** rule on the bucket.
- To remove only some, write the full document without them with
  `aws s3api put-bucket-lifecycle-configuration`.

Remove the rule from the config first. Otherwise the next run of the cluster phase adds it
back.

## Check that it works

Read the rules back with the AWS CLI. The Compose file publishes port 8333, so you can run it
from your machine with the admin identity's keys:

```console
$ AWS_ACCESS_KEY_ID=<admin-access-key> AWS_SECRET_ACCESS_KEY=<admin-secret-key> AWS_DEFAULT_REGION=us-east-1 \
    aws --endpoint-url http://localhost:8333 s3api get-bucket-lifecycle-configuration --bucket backups
{
    "TransitionDefaultMinimumObjectSize": "all_storage_classes_128K",
    "Rules": [
        {
            "Expiration": {
                "Days": 14
            },
            "ID": "expire-old-snapshots",
            "Filter": {
                "Prefix": "snapshots/"
            },
            "Status": "Enabled"
        }
    ]
}
```

This is the output after step 3, captured against `chrislusf/seaweedfs:latest` (SeaweedFS
4.53) on 2026-11-15. After step 4, `Status` reads `Disabled`. That version returns the
prefix inside `Filter`. The provisioner also reads the older top-level `Prefix` shape, which
other SeaweedFS versions can return, so either one compares as up to date.

A bucket without any rules answers with an error rather than an empty list:

```console
An error occurred (NoSuchLifecycleConfiguration) when calling the GetBucketLifecycleConfiguration operation: The lifecycle configuration does not exist
```

## Pitfalls

- **Extra settings on hand-made rules disappear on the next write.** The provisioner carries
  only `id`, prefix, expiration days and status of every rule on the bucket. When it writes,
  anything else is dropped — also on rules it doesn't manage. In a test, a rule created by
  hand with `AbortIncompleteMultipartUpload` and `NoncurrentVersionExpiration` survived runs
  in which nothing changed, and lost both elements on the first run that changed a
  configured rule. If you need such settings, keep them on a bucket the provisioner has no
  `lifecycle` rules for.
- **`strategy: create` skips lifecycle on an existing bucket.** The provisioner logs
  `Skipping existing bucket and its settings (strategy=create)` and doesn't read or write the
  rules. A bucket created in the same run still gets them. See
  [Strategy: update or create](../concepts.md#strategy-update-or-create).
- **An empty `lifecycle` list changes nothing.** Leaving out `lifecycle`, or listing no
  rules, doesn't clear the rules on the bucket; it skips the lifecycle calls altogether.
- **The provisioner doesn't decide when objects disappear.** It stores the rule; SeaweedFS
  runs the expiration on its own schedule. Check the
  [SeaweedFS documentation](https://github.com/seaweedfs/seaweedfs/wiki) for your version
  before you rely on exact timing.
- **Validation errors stop the run before anything is applied.** A missing `id`, a
  duplicate `id` in the same bucket, a status other than lowercase `enabled` or `disabled`,
  or `expiration_days` of 0 or less fails config loading, for example
  `buckets[0].lifecycle[0].expiration_days: must be greater than 0`. A misspelled
  `expiration_days` key is silently ignored by the YAML parser and then fails with that same
  message. See [Validation](../configuration.md#validation).

## Further reading

- [Lifecycle rules](../configuration.md#lifecycle-rules) in the configuration reference.
- [What "update" means for each resource](../concepts.md#what-update-means-for-each-resource)
  for the merge in detail.
- [Dry-run](../concepts.md#dry-run) to preview a lifecycle change before applying it.
- [Control bucket access with a policy](control-bucket-access-with-a-policy.md), the other
  bucket setting the cluster phase manages.
