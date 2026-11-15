# Configuration reference

The complete reference for the YAML config file. For a guided introduction, start with
[Getting started](getting-started.md). A commented example with most fields is in
[config.example.yaml](../config.example.yaml).

- [Top level](#top-level)
- [Buckets](#buckets): [versioning](#versioning), [lifecycle rules](#lifecycle-rules)
- [Identities](#identities): [actions](#actions)
- [Policies](#policies)
- [Environment variable expansion](#environment-variable-expansion): [unset variables](#unset-variables)
- [Validation](#validation)

The provisioner reads the file named by `SEAWEEDFS_CONFIG_PATH` (default `./config.yaml`, which
is `/config.yaml` in the container image). It loads and validates the whole file in every
phase, so a mistake in a bucket also fails the `identities` phase. See
[Environment variables](deployment.md#environment-variables).

## Top level

```yaml
strategy: update     # optional: "update" (default) or "create"
buckets: [...]       # optional
identities: [...]    # optional
policies: [...]      # optional
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `strategy` | string | no | `update` | Default strategy for all buckets. `update` creates missing buckets and reconciles the settings of existing ones; `create` creates missing buckets and leaves existing ones, including their settings, alone. Accepts `${VAR}`. |
| `buckets` | list | no | none | Buckets to create, see [Buckets](#buckets). |
| `identities` | list | no | none | Identities written into `s3.config`, see [Identities](#identities). |
| `policies` | list | no | none | Bucket policies, see [Policies](#policies). |

Strategy applies to buckets only: identities are written on every run, and policies are
always compared with their file. See
[Strategy: update or create](concepts.md#strategy-update-or-create).

Every list is optional. An empty config file is valid and makes a run that changes nothing.

## Buckets

```yaml
buckets:
  - name: documents          # required
    strategy: update         # optional, overrides the top-level strategy
    versioning: true         # optional: true, false, or omit
    lifecycle:               # optional
      - id: expire-tmp
        prefix: "tmp/"
        expiration_days: 7
```

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `name` | string | yes | | Bucket name. Accepts `${VAR}`. |
| `strategy` | string | no | top-level `strategy`, then `update` | `update` or `create`. Accepts `${VAR}`. |
| `versioning` | bool | no | omitted | See [Versioning](#versioning). Does not accept `${VAR}`. |
| `lifecycle` | list | no | none | Expiration rules, see [Lifecycle rules](#lifecycle-rules). |

**Name rules.** A bucket name is 3 to 63 characters of lowercase letters, digits and hyphens,
and starts and ends with a letter or digit (`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`). Dots and
uppercase letters are rejected, although S3 itself allows dots. Each name may appear only once.

**Order.** Buckets are processed in the order listed, before any policy. The run stops at the
first bucket that fails.

**`strategy: create` on an existing bucket** skips the bucket and its versioning and
lifecycle settings, and logs `Skipping existing bucket and its settings (strategy=create)`. A
bucket that does not exist yet is created and gets its settings under either strategy.

### Versioning

`versioning` has three states, because "not set" and "off" mean different things:

| Value | What the provisioner does |
|---|---|
| omitted | Nothing. It doesn't read or change the bucket's versioning. |
| `true` | Enables versioning unless it is already `Enabled`. |
| `false` | Suspends versioning, but only if it is currently `Enabled`. |

`false` never writes to a bucket that has never been versioned: SeaweedFS reports no status
for such a bucket, which already counts as "not enabled". To leave versioning that someone
enabled by hand untouched, omit the field instead of writing `false`.

`versioning` is a YAML boolean, so `${VAR}` doesn't work here. `versioning: ${V}` fails at
parse time with ``cannot unmarshal !!str `${V}` into bool``.

### Lifecycle rules

Lifecycle rules make SeaweedFS expire objects a number of days after they are written,
optionally only under a key prefix.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `id` | string | yes | | Rule ID. The provisioner matches rules on the bucket by this ID. Unique per bucket. Accepts `${VAR}`. |
| `expiration_days` | integer | yes | | Days after which objects expire. Must be greater than 0. Does not accept `${VAR}`. |
| `prefix` | string | no | `""` (whole bucket) | Key prefix the rule applies to. Accepts `${VAR}`. |
| `status` | string | no | `enabled` | `enabled` or `disabled`, lowercase. Accepts `${VAR}`. |

How a run applies them:

- **Omitted or empty `lifecycle`:** no lifecycle call at all. Rules already on the bucket stay.
- **Merge by `id`:** S3 replaces the whole lifecycle document on every write, so the
  provisioner reads the current rules, replaces those whose `id` matches a configured rule,
  appends the new ones, and keeps all others. Rules you didn't configure survive.
- **Drift check:** it writes only when the merged set differs from the current one. Rule order
  doesn't matter.
- **What is carried over:** the provisioner models a rule as `id`, prefix, expiration in days
  and status, and writes back only those. When it rewrites the document, every other element
  of an existing rule is dropped, also on rules it doesn't manage: transitions, tag filters,
  date-based expiration, noncurrent-version expiration and incomplete-multipart-upload
  cleanup. If other tools put such rules on a bucket, don't also configure `lifecycle` for it.
- **Disabling:** the provisioner never deletes a rule. Set `status: disabled` to turn one off.
  Removing a rule from the YAML leaves it on the bucket, active.

See [Expire objects with lifecycle rules](howto/expire-objects-with-lifecycle-rules.md).

## Identities

An identity is a named set of access keys plus the actions they allow. The provisioner writes
identities into the `s3.config` file that the SeaweedFS S3 gateway loads at startup. See
[Why identities are a file](concepts.md#why-identities-are-a-file).

```yaml
identities:
  - name: app-service                          # required, unique
    credentials:                               # required, at least one
      - access_key: ${APP_SERVICE_ACCESS_KEY}
        secret_key: ${APP_SERVICE_SECRET_KEY}
    actions: ["Read:documents", "Write:documents", "List:documents"]   # required, at least one
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Identity name, unique in the file. Bucket policies refer to it as `arn:aws:iam::000000000000:user/<name>`. Accepts `${VAR}`. |
| `credentials` | list | yes, at least one | Access key pairs. |
| `credentials[].access_key` | string | yes | Access key ID. Accepts `${VAR}`. |
| `credentials[].secret_key` | string | yes | Secret access key. Accepts `${VAR}`. |
| `actions` | list of strings | yes, at least one | See [Actions](#actions). Each entry accepts `${VAR}`. |

How a run applies them:

- **Every listed identity is rewritten on every run**, without a drift check. The phase logs
  `Writing identity` and `Identity written` for each one.
- **Replaced as a whole, by name.** The entry in `s3.config` gets exactly the credentials and
  actions from the YAML. A credential you remove from the list is removed from the file; this
  is how you retire an old key after
  [rotating it](howto/rotate-an-access-key.md).
- **Identities not in the YAML stay** in the file, and so do unknown top-level keys such as
  `accounts`.
- **Other fields are not kept.** The provisioner re-encodes the identity list with only
  `name`, `credentials` (`accessKey`, `secretKey`) and `actions`. Any other field, on any
  identity or credential in the file, including identities the provisioner doesn't manage,
  is dropped on write.
- **Changes need a SeaweedFS restart.** The gateway reads `s3.config` only at startup. See
  [Change identities on a running cluster](howto/change-identities-on-a-running-cluster.md).

Once `s3.config` holds any identity, the gateway requires authentication. The cluster phase
then needs the keys of an identity that may list and create buckets and set policies: see
[Set up the admin identity](howto/set-up-the-admin-identity.md).

### Actions

An action is either a bare prefix, which applies to all buckets, or `<Prefix>:<bucket>`, which
applies to one bucket:

```yaml
actions:
  - "Admin"            # all buckets
  - "Read:backups"     # only the bucket "backups"
```

The provisioner accepts six prefixes: `Read`, `Write`, `List`, `Tagging`, `Lock` and `Admin`.
Validation is exact and case-sensitive:

- An action must be exactly one of the six prefixes, or a prefix followed by `:`.
  `read`, `READ` and `s3:GetObject` are rejected.
- The part after `:` is not checked. `Read:no-such-bucket` and `Read:` are accepted, and the
  bucket doesn't have to be declared in `buckets` or exist.

The provisioner copies the strings into `s3.config` unchanged. What each action allows is
decided by SeaweedFS when it authorizes a request, not by the provisioner. The names suggest
reading objects, writing objects, listing, object tagging, object locking, and administrative
access; check the SeaweedFS documentation for the exact meaning.

Verified on 2026-11-15 against `chrislusf/seaweedfs:latest`: an identity with `Admin`, `Read`,
`Write`, `List` and `Tagging` could run the cluster phase (create a bucket, enable versioning,
set a policy), and an identity with `Read:documents`, `Write:documents` and `List:documents`
could upload to and list the bucket `documents`.

## Policies

A policy entry attaches a bucket policy, a JSON document in the AWS S3 policy language, to a
bucket. The document lives in its own file:

```yaml
policies:
  - bucket: documents                       # required, one policy per bucket
    file: policies/documents-policy.json    # required
```

| Field | Type | Required | Description |
|---|---|---|---|
| `bucket` | string | yes | Bucket to attach the policy to. At most one entry per bucket. Accepts `${VAR}`. |
| `file` | string | yes | Path to the JSON policy document. Accepts `${VAR}`; the file's content does not. |

- **Path resolution:** a relative `file` resolves against the process's working directory, not
  against the config file. The container image has no working directory set, so it is `/`, and
  `policies/documents-policy.json` means `/policies/documents-policy.json`. Mount your policy
  directory at `/policies`, or use absolute paths.
- **When the file is read:** only in the cluster phase, after all buckets. Validation does not
  check that the file exists. A missing file fails the run with
  `reading policy file "<file>": ...`, invalid JSON with `policy file "<file>" is not valid JSON`.
- **The bucket must exist.** Validation does not check `bucket` against `buckets`. Declare the
  bucket there, so it is created before the policy is applied.
- **Principals:** SeaweedFS matches an identity only as
  `arn:aws:iam::000000000000:user/<identity-name>`. The shorter form
  `arn:aws:iam:::user/<identity-name>` is accepted when the policy is set but never matches
  any request, so it has no effect
  (verified against SeaweedFS 4.53). The provisioner doesn't check principals.
- **Policies and actions are evaluated independently** by SeaweedFS: an `Allow` in a bucket
  policy can grant more than the identity's [actions](#actions), and a `Deny` can block what
  the actions allow.
- **Drift check:** the provisioner reads the current policy back from SeaweedFS and compares
  both documents with whitespace removed. Reformatting the file changes nothing. Anything else
  that differs counts as a change and rewrites the policy: reordered keys, different string
  escaping, and also a one-element `Action` or `Resource` list, which SeaweedFS returns as a
  plain string. A policy written with such lists is therefore rewritten on every run; write
  single values as strings to avoid that. A bucket without a policy always gets one.
- **Strategy doesn't apply.** Policies are reconciled on every run, also for buckets with
  `strategy: create`.
- The provisioner never removes a policy. Removing an entry from the YAML leaves the policy on
  the bucket.

See [Control bucket access with a policy](howto/control-bucket-access-with-a-policy.md).

## Environment variable expansion

String fields can reference environment variables as `${VAR}`. The provisioner replaces them
after parsing the YAML and before validating it.

```yaml
access_key: ${APP_SERVICE_ACCESS_KEY}      # whole value
name: "${ENVIRONMENT}-documents"           # part of a value
```

The rules are deliberately narrow:

| You write | Result |
|---|---|
| `${VAR}`, `VAR` set | The value of `VAR`, inserted as-is. YAML special characters in it need no quoting, because the YAML is already parsed. |
| `${VAR}`, `VAR` set to an empty string | An empty string. On a required field, validation fails with `is required`. |
| `${VAR}`, `VAR` not set | **The run fails** with `environment variable "VAR" is not set`, if the current phase uses the field. See [Unset variables](#unset-variables). |
| `${VAR:-default}` | Not supported. Treated as a variable named `VAR:-default`, which is normally unset, so the run fails even when `VAR` is set. |
| `$VAR` | Not expanded. |
| `$${VAR}` | No escape syntax. The `${VAR}` part is expanded and the leading `$` stays. |

- There is no `*_FILE` variant: the provisioner never reads a variable's value from a file.
  To keep keys out of the config, see
  [Keep access keys out of the config file](howto/keep-access-keys-out-of-the-config.md).
- Expansion is a single pass; a value that contains `${...}` is not expanded again.
- **Fields that accept `${VAR}`:** top-level and bucket `strategy`, bucket `name`, lifecycle
  `id`, `prefix` and `status`, identity `name`, `access_key`, `secret_key`, each entry of
  `actions`, and policy `bucket` and `file`.
- **Fields that don't:** `versioning` and `expiration_days`. They are typed in YAML, so
  `${VAR}` there fails parsing, for example
  ``line 5: cannot unmarshal !!str `${DAYS}` into int``.
- The content of policy files is not expanded.

### Unset variables

A `${VAR}` reference to a variable that isn't set stops the run before anything is changed,
in the phase that uses the field. Each phase checks only its own fields, so a container
needs only the variables its phase reads:

| Phase | Fields checked for unset variables |
|---|---|
| `identities` | Everything under `identities` |
| `cluster` | Top-level `strategy`, everything under `buckets` and `policies` |
| `all` | Every field |

A reference in a field the phase doesn't use stays as literal text, and is still validated.
The cluster phase, for example, runs without the identities' key variables. A bucket named
`${ENVIRONMENT}-docs`, however, fails the identities phase with
`invalid bucket name "${ENVIRONMENT}-docs"` when `ENVIRONMENT` isn't set there, so pass
variables used in bucket names to both phases. The error names
the field and the variable, and the process exits with code `1`:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: expanding environment variables: identities[1].credentials[0].secret_key: environment variable \"REPORTS_SECRET_KEY\" is not set"}
```

The run stops at the first unset variable it finds, and `--dry-run` fails the same way. A
variable set to an empty string counts as set.

Up to 1.0.0, an unset variable was not an error: `${VAR}` stayed as literal text, passed
validation, and an unset `${APP_SERVICE_ACCESS_KEY}` became the access key
`${APP_SERVICE_ACCESS_KEY}` in `s3.config`.

## Validation

The provisioner validates the config before it touches anything, in every phase. It stops at
the first error, so fix it and run again to see the next one. Errors name the path of the
field and are logged as:

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: <path>: <message>"}
```

The process then exits with code `1`.

| Path | Message | Triggered by |
|---|---|---|
| `strategy` | `invalid strategy "<value>" (must be "create" or "update")` | Any other value, including `Update` |
| `buckets[i].strategy` | `invalid strategy "<value>" (must be "create" or "update")` | Same, per bucket |
| `buckets[i].name` | `is required` | Missing or empty name |
| `buckets[i].name` | `contains null byte` | A `\0` in the name |
| `buckets[i].name` | `invalid bucket name "<name>" (must be 3-63 chars, lowercase alphanumeric and hyphens)` | See [name rules](#buckets) |
| `buckets[i].name` | `duplicate bucket name "<name>"` | Same name twice |
| `buckets[i].lifecycle[j].id` | `is required` | Missing or empty `id` |
| `buckets[i].lifecycle[j].id` | `contains null byte` | A `\0` in the ID |
| `buckets[i].lifecycle[j].id` | `duplicate lifecycle rule id "<id>"` | Same ID twice in one bucket |
| `buckets[i].lifecycle[j].prefix` | `contains null byte` | A `\0` in the prefix |
| `buckets[i].lifecycle[j].status` | `invalid status "<value>" (must be "enabled" or "disabled")` | Anything else, including `Enabled` |
| `buckets[i].lifecycle[j].expiration_days` | `must be greater than 0` | Zero, negative, or missing, also when the key is misspelled |
| `identities[i].name` | `is required` | Missing or empty name |
| `identities[i].name` | `contains null byte` | A `\0` in the name |
| `identities[i].name` | `duplicate identity name "<name>"` | Same name twice |
| `identities[i].credentials` | `at least one credential is required` | Missing or empty list |
| `identities[i].credentials[j].access_key` | `is required` | Missing, empty, or a `${VAR}` set to empty |
| `identities[i].credentials[j].access_key` | `contains null byte` | A `\0` in the key |
| `identities[i].credentials[j].secret_key` | `is required` | Missing, empty, or a `${VAR}` set to empty |
| `identities[i].credentials[j].secret_key` | `contains null byte` | A `\0` in the key |
| `identities[i].actions` | `at least one action is required` | Missing or empty list |
| `identities[i].actions[j]` | `invalid action "<action>" (must start with Read, Write, List, Tagging, Lock, or Admin)` | See [Actions](#actions) |
| `policies[i].bucket` | `is required` | Missing or empty bucket |
| `policies[i].bucket` | `contains null byte` | A `\0` in the bucket |
| `policies[i].bucket` | `duplicate policy for bucket "<bucket>"` | Two policies for one bucket |
| `policies[i].file` | `is required` | Missing or empty file |
| `policies[i].file` | `contains null byte` | A `\0` in the path |

Real output from `seaweedfs-provisioner --phase=cluster` with broken configs (real lines also
carry `time`):

```json
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: buckets[0].name: invalid bucket name \"My.Bucket\" (must be 3-63 chars, lowercase alphanumeric and hyphens)"}
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: buckets[0].lifecycle[0].status: invalid status \"Enabled\" (must be \"enabled\" or \"disabled\")"}
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: identities[0].credentials[0].access_key: is required"}
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: identities[0].actions[0]: invalid action \"s3:GetObject\" (must start with Read, Write, List, Tagging, Lock, or Admin)"}
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: policies[1].bucket: duplicate policy for bucket \"docs\""}
```

Errors before validation have their own prefixes:

- `loading configuration: reading config file: ...` when the file can't be read, for example
  `open /config.yaml: no such file or directory`.
- `loading configuration: parsing config YAML: ...` for invalid YAML or a wrong type, for
  example ``yaml: unmarshal errors:\n  line 3: cannot unmarshal !!str `${V}` into bool``.
- `loading configuration: expanding environment variables: <path>: environment variable "<VAR>" is not set`
  for an unset variable; see [Unset variables](#unset-variables).

What validation does **not** check:

- **Unknown keys are ignored.** A misspelled key, such as `bukets:` or `credentails:`, is
  silently skipped, and its content with it. A misspelled required key still shows up as
  "is required"; a misspelled optional key doesn't show up at all.
- Unset `${VAR}` references in fields the current phase doesn't use (see
  [Unset variables](#unset-variables)).
- That buckets in scoped actions or in `policies` are declared or exist.
- That policy files exist or contain valid JSON. That is checked in the cluster phase.
- Whether SeaweedFS accepts the values. Errors from SeaweedFS appear during the run.

See also the [FAQ on configuration errors](faq.md#configuration-errors).
