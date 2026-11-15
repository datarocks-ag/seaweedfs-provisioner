# Control bucket access with a policy

After this you can attach an S3 bucket policy from a JSON file to a bucket, keep it in sync
on every run, and check that SeaweedFS enforces it.

## When you need this

Identity `actions` in `s3.config` work per bucket at best: `Read:documents` reads every
object in `documents`. Sometimes you need finer control, such as a reporting tool that may
read only `documents/reports/` and nothing else. A bucket policy describes access per key
prefix and per S3 operation, and keeping it in a file next to the config means it is
reviewed and applied like everything else.

## What you need to know first

- **Bucket policy.** A JSON document attached to a bucket that allows or denies S3
  operations (`s3:GetObject`, `s3:PutObject`, …) on resources for principals. The format is
  the AWS one; see
  [Policies and permissions in Amazon S3](https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-policy-language-overview.html).
- **Policies and identity actions both count.** In a test against `chrislusf/seaweedfs:latest`
  (SeaweedFS 4.53) on 2026-11-15, SeaweedFS let an identity with only `List:documents` in
  `s3.config` read an object that the bucket policy allowed it to read, and an explicit
  `Deny` blocked an identity whose actions allowed the operation. Test the behavior you rely
  on against the SeaweedFS version you run. See [Actions](../configuration.md#actions).
- **Principal format.** In that test, a statement matched an identity only when the
  principal was `arn:aws:iam::000000000000:user/<identity-name>`. The shorter form
  `arn:aws:iam:::user/<identity-name>`, which releases up to 1.0.0 used in the example
  `policies/documents-policy.json`, was accepted by SeaweedFS but did not match.
- **Cluster phase.** The provisioner reads the policy file and sends it over the S3 API in
  the `cluster` phase, after the buckets. See [Two phases](../concepts.md#two-phases).

The examples use the bucket `documents`, the identity `report-reader` and the Docker Compose
setup from [Getting started](../getting-started.md#step-3-create-a-bucket). Replace them with
your own names.

## Steps

### 1. Write the policy file

Create `policies/documents-policy.json` next to your config. This one lets `report-reader`
download objects under `reports/` in `documents`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReportReaderReadReports",
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::000000000000:user/report-reader" },
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::documents/reports/*"
    }
  ]
}
```

The keys are in the order SeaweedFS returns them, and single values are written as strings,
not one-element lists. That keeps the provisioner from rewriting the policy on every run;
the pitfalls below explain why.

### 2. Declare the bucket, the identity and the policy

The policy's bucket must exist when the cluster phase reaches the policies, so declare it
under `buckets`. The identity gets only `List:documents`; reading comes from the policy:

```yaml
buckets:
  - name: documents

identities:
  - name: admin                       # the cluster phase logs in with this identity
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions: ["Admin", "Read", "Write", "List", "Tagging"]
  - name: report-reader
    credentials:
      - access_key: ${REPORT_READER_ACCESS_KEY}
        secret_key: ${REPORT_READER_SECRET_KEY}
    actions: ["List:documents"]

policies:
  - bucket: documents
    file: policies/documents-policy.json
```

New identities take effect only when SeaweedFS starts, so run the identities phase and
restart SeaweedFS if `report-reader` is new. See
[Change identities on a running cluster](change-identities-on-a-running-cluster.md).

### 3. Mount the policy directory at /policies

`file` is resolved against the provisioner's working directory, not against the config
file. The image has no working directory set, so it is `/`, and
`policies/documents-policy.json` means `/policies/documents-policy.json` in the container.
Mount the directory there on the cluster-phase service:

```yaml
  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=cluster"]
    # … environment as in Getting started
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./policies:/policies:ro
```

### 4. Run the cluster phase

```console
$ docker compose run --rm seaweedfs-provisioner
```

On the first run the provisioner creates the bucket and sets the policy. Output is trimmed;
real lines also carry `time`:

```json
{"level":"INFO","msg":"Creating bucket","bucket":"documents"}
{"level":"INFO","msg":"Setting bucket policy","bucket":"documents","file":"policies/documents-policy.json"}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Run it again with `LOG_LEVEL=debug`, and it finds nothing to change:

```console
$ docker compose run --rm -e LOG_LEVEL=debug seaweedfs-provisioner
```

```json
{"level":"DEBUG","msg":"Bucket already exists","bucket":"documents"}
{"level":"DEBUG","msg":"Bucket policy up to date","bucket":"documents"}
```

## Check that it works

First read the policy back. The Compose file publishes port 8333, so you can run the AWS CLI
from your machine, here with the admin identity's keys:

```console
$ AWS_ACCESS_KEY_ID=<admin-access-key> AWS_SECRET_ACCESS_KEY=<admin-secret-key> AWS_DEFAULT_REGION=us-east-1 \
    aws --endpoint-url http://localhost:8333 s3api get-bucket-policy --bucket documents
{
    "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"ReportReaderReadReports\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::000000000000:user/report-reader\"},\"Action\":\"s3:GetObject\",\"Resource\":\"arn:aws:s3:::documents/reports/*\"}]}\n"
}
```

Then check enforcement with `report-reader`'s keys. First, as admin, upload a local file
`report.txt` (containing `quarterly numbers`) twice, as `reports/q3.txt` and `drafts/q4.txt`.
Then set `report-reader`'s keys in the environment:

```console
$ export AWS_ACCESS_KEY_ID=<report-reader-access-key> AWS_SECRET_ACCESS_KEY=<report-reader-secret-key> AWS_DEFAULT_REGION=us-east-1
```

A download under `reports/` works, because the policy allows it:

```console
$ aws --endpoint-url http://localhost:8333 s3 cp s3://documents/reports/q3.txt -
quarterly numbers
```

A download outside `reports/` is refused, and so is an upload, because neither the policy
nor the identity's actions allow it:

```console
$ aws --endpoint-url http://localhost:8333 s3 cp s3://documents/drafts/q4.txt -
download failed: s3://documents/drafts/q4.txt to - An error occurred (403) when calling the HeadObject operation: Forbidden
$ aws --endpoint-url http://localhost:8333 s3 cp report.txt s3://documents/reports/q4.txt
upload failed: ./report.txt to s3://documents/reports/q4.txt An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.
```

Listing works through the identity's `List:documents` action:

```console
$ aws --endpoint-url http://localhost:8333 s3 ls s3://documents/reports/
2026-11-15 16:53:02         18 q3.txt
```

These results were captured against SeaweedFS 4.53. If the first download is also refused,
check the principal format first.

## Pitfalls

- **The policy is applied but nothing changes.** With the principal
  `arn:aws:iam:::user/report-reader`, the run logged `Setting bucket policy`, and the
  download above still failed with `403 Forbidden`. Use
  `arn:aws:iam::000000000000:user/<identity-name>`, as in step 1.
- **`Setting bucket policy` on every run.** The provisioner compares the policy SeaweedFS
  returns with your file, ignoring only whitespace. SeaweedFS stores the policy in its own
  shape, so two things look like a change every time: keys in a different order than
  SeaweedFS returns them, and one-element lists such as `"Action": ["s3:GetObject"]`, which
  come back as `"Action": "s3:GetObject"`. Lists with two or more entries stay lists. To
  fix it, compare your file with `get-bucket-policy` and match its shape. Reformatting
  and indenting the file is not a change.
- **`reading policy file`.** The path doesn't exist inside the container, often because the
  directory isn't mounted at `/policies` or the name has a typo:
  `provisioning policy for bucket "documents": reading policy file "policies/document-policy.json": open policies/document-policy.json: no such file or directory`.
  The file is read only in the cluster phase, so the identities phase succeeds with the same
  mistake.
- **`is not valid JSON`.** The file doesn't parse:
  `provisioning policy for bucket "documents": policy file "policies/documents-policy.json" is not valid JSON`.
  Environment variables are not expanded inside the policy file, so `${…}` stays literal
  text.
- **`NoSuchBucket`.** Validation doesn't check that a policy's bucket is listed under
  `buckets`. If the bucket doesn't exist, the run fails when it sets the policy:
  `… putting bucket policy for "<bucket>": … api error NoSuchBucket: The specified bucket does not exist`.
- **`InvalidPolicyDocument`.** SeaweedFS refused a policy whose `Resource` names a different
  bucket than the one it is attached to, with
  `The content of the policy document is invalid.` Each bucket needs its own file; the
  config allows one policy per bucket.
- **A `Deny` for `"Principal": "*"` also stops the admin identity.** In the test, a statement
  denying `s3:PutObject` to `*` on a prefix blocked uploads by the admin identity too.
- **`strategy` doesn't apply.** Policies are reconciled against the file on every run,
  including for buckets with `strategy: create`. See
  [Strategy: update or create](../concepts.md#strategy-update-or-create).
- **Removing the policy from the config leaves it on the bucket.** The provisioner never
  deletes a policy. Remove it with the admin identity's keys and
  `aws s3api delete-bucket-policy --bucket <bucket>`; afterwards `get-bucket-policy` answers
  `NoSuchBucketPolicy`. See [Additive by design](../concepts.md#additive-by-design).

## Further reading

- [Policies](../configuration.md#policies) in the configuration reference.
- [Give an application its own credentials](give-an-application-its-own-credentials.md) for
  identity actions, the other access layer.
- [Expire objects with lifecycle rules](expire-objects-with-lifecycle-rules.md), the other
  bucket setting the cluster phase manages.
- [Bucket policy examples](https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-bucket-policies.html)
  in the Amazon S3 docs; check each one against SeaweedFS before relying on it.
