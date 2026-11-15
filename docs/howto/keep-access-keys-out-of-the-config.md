# Keep access keys out of the config file

After this `config.yaml` holds only `${VAR}` placeholders, the keys come from Docker Compose
or a Kubernetes Secret, and a missing key stops the identities phase before it writes
`s3.config`.

## When you need this

`config.yaml` is usually committed next to the deployment. Access and secret keys written
into it end up in version control, in reviews and in every copy of the repository. The
keys belong in your secret store; the config should only say where each one goes.

## What you need to know first

- **`${VAR}` expansion.** When the provisioner loads the config, it replaces `${<NAME>}` with
  the value of the environment variable `<NAME>`. Access keys, secret keys, identity names
  and actions are expanded, among other string fields. See
  [Environment variable expansion](../configuration.md#environment-variable-expansion).
- **An unset variable stops the run.** If `<NAME>` is not set, the phase that uses the field
  fails with `environment variable "<NAME>" is not set` and writes nothing. A variable set
  to an empty string is rejected too, with `is required`. Up to 1.0.0, an unset variable
  was not an error: `${<NAME>}` was written into `s3.config` as the real key.
- **Only environment variables.** The provisioner has no `<NAME>_FILE` variant and reads no
  secret files. The image is built from `scratch`, so it has no shell either that could
  read a file into a variable. Keys have to arrive as environment variables.
- **No defaults.** `${<NAME>:-<default>}` is not supported. The whole text, including
  `:-<default>`, is taken as the variable name, which never exists, so the run fails even
  when `<NAME>` is set.

The keys end up in plain text in `s3.config` anyway, because that is the file SeaweedFS
reads. To limit who can read that file, see
[Restrict who can read s3.config](restrict-who-can-read-s3-config.md).

The examples use the identities `admin` and `reports-app` and the Docker Compose setup from
[Getting started](../getting-started.md#step-2-render-s3config-and-start-seaweedfs). The
output was captured on 2026-11-15, with `seaweedfs-provisioner` 1.0.0 and, for the
unset-variable errors, a build of the release that follows it.

## Steps

### 1. Use a placeholder for every key

Write every access key and secret key as a placeholder:

```yaml
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

Only the identities phase writes keys. The cluster phase loads and validates the same file
but uses none of these values, so it doesn't need the identity variables and doesn't check
whether they are set. Leave them unset there rather than set to an empty string, which fails
validation.

### 2. Docker Compose: pass the keys from an env file

Put the keys in a file outside version control and hand it to the identities service with
`env_file`:

```text
# secrets/seaweedfs.env — add secrets/ to .gitignore
SEAWEEDFS_ADMIN_ACCESS_KEY=<admin-access-key>
SEAWEEDFS_ADMIN_SECRET_KEY=<admin-secret-key>
REPORTS_ACCESS_KEY=<reports-access-key>
REPORTS_SECRET_KEY=<reports-secret-key>
```

```yaml
services:
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    env_file: secrets/seaweedfs.env
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfsconfig:/seaweedfs-config
```

The cluster phase needs the admin pair under different names, `SEAWEEDFS_ACCESS_KEY` and
`SEAWEEDFS_SECRET_KEY`. Docker Compose can substitute those from the same values when they
are also in the `.env` file next to `docker-compose.yaml`; see
[Set up the admin identity the provisioner logs in with](set-up-the-admin-identity.md).

Compose `secrets:` doesn't work here: they arrive as files under `/run/secrets/`, and the
provisioner reads no files.

### 3. Kubernetes: map Secret keys to environment variables

> Not captured: this manifest was not run against a cluster for this guide.

Reference each key with `secretKeyRef` in the identities init container. With a Secret
`seaweedfs-identities` that holds the four keys:

```yaml
      initContainers:
        - name: seaweedfs-identities
          image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
          args: ["--phase=identities"]
          env:
            - name: SEAWEEDFS_CONFIG_PATH
              value: /config/config.yaml
            - name: SEAWEEDFS_S3_CONFIG_PATH
              value: /seaweedfs-config/s3.json
            - name: SEAWEEDFS_ADMIN_ACCESS_KEY
              valueFrom:
                secretKeyRef: { name: seaweedfs-identities, key: admin-access-key }
            - name: SEAWEEDFS_ADMIN_SECRET_KEY
              valueFrom:
                secretKeyRef: { name: seaweedfs-identities, key: admin-secret-key }
            - name: REPORTS_ACCESS_KEY
              valueFrom:
                secretKeyRef: { name: seaweedfs-identities, key: reports-access-key }
            - name: REPORTS_SECRET_KEY
              valueFrom:
                secretKeyRef: { name: seaweedfs-identities, key: reports-secret-key }
          # … volumeMounts for the config and the s3.config volume
```

`secretKeyRef` fails the container start if the Secret or the key doesn't exist (with the
default `optional: false`). A variable you forget to list fails the identities phase
instead, as in the next step. The full layout is in
[Deployment → Kubernetes](../deployment.md#kubernetes).

### 4. Read the error when a variable is missing

The identities phase expands every key before it writes anything. Here `REPORTS_SECRET_KEY`
was not set. Output is trimmed; real lines also carry `time`:

```json
{"level":"INFO","msg":"Loading configuration","path":"/config.yaml"}
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: expanding environment variables: identities[1].credentials[0].secret_key: environment variable \"REPORTS_SECRET_KEY\" is not set"}
```

The process exits with code `1` and leaves `s3.config` as it was. The path names the field,
here the secret key of the second identity, and the message names the variable. The run
stops at the first missing variable, so set it and run again to see the next one. In Docker
Compose, a failed identities service keeps SeaweedFS from starting through
`service_completed_successfully`.

## Check that it works

Run the identities phase. It ends with `seaweedfs-provisioner finished successfully` and
exit code `0`:

```console
$ docker compose run --rm seaweedfs-identities
$ echo $?
0
```

Then restart SeaweedFS and log in with each identity's keys, as in
[Give an application its own credentials](give-an-application-its-own-credentials.md).
`InvalidAccessKeyId` or `SignatureDoesNotMatch` for a key that should work points to a value
that differs between the identities phase and the client.

## Pitfalls

- **A dry run catches it too.** `--dry-run` loads the config the same way, so a missing
  variable fails the preview with the same error.
- **`${<NAME>:-<default>}` fails even when `<NAME>` is set.** With
  `secret_key: ${REPORTS_SECRET_KEY:-changeme}` and `REPORTS_SECRET_KEY` set, the run failed
  with `environment variable "REPORTS_SECRET_KEY:-changeme" is not set`. Use plain
  `${<NAME>}`.
- **Version 1.0.0 writes the placeholder.** With 1.0.0, a missing variable is not an error,
  and `s3.config` holds, for example, `"secretKey": "${REPORTS_SECRET_KEY}"`. If the admin
  key is the placeholder, the cluster phase gets `InvalidAccessKeyId` and retries for five
  minutes. Until you upgrade, search the file for `${` after every identities phase, from a
  container that mounts the `s3.config` volume:
  `docker compose exec seaweedfs grep -n '${' /seaweedfs-config/s3.json`. No output and exit
  code `1` means every placeholder was expanded.
- **An empty variable fails the run.** `REPORTS_SECRET_KEY=` (set, but empty) stops the run
  before anything is written:

  ```json
  {"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: identities[1].credentials[0].secret_key: is required"}
  ```

- **Keys in `docker inspect`.** Environment variables are visible to anyone who can inspect
  the container. That is the price of having no file-based input.

## Further reading

- [Environment variable expansion](../configuration.md#environment-variable-expansion) and
  [Validation](../configuration.md#validation) in the configuration reference.
- [Environment variables](../deployment.md#environment-variables) and
  [Kubernetes](../deployment.md#kubernetes) in the deployment guide.
- [FAQ: Configuration errors](../faq.md#configuration-errors).
- [Restrict who can read s3.config](restrict-who-can-read-s3-config.md).
