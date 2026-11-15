# Keep access keys out of the config file

After this `config.yaml` holds only `${VAR}` placeholders, the keys come from Docker Compose
or a Kubernetes Secret, and you can tell when a placeholder was written into `s3.config`
instead of a key.

## When you need this

`config.yaml` is usually committed next to the deployment. Access and secret keys written
into it end up in version control, in reviews and in every copy of the repository. The
keys belong in your secret store; the config should only say where each one goes.

## What you need to know first

- **`${VAR}` expansion.** When the provisioner loads the config, it replaces `${<NAME>}` with
  the value of the environment variable `<NAME>`. Access keys, secret keys, identity names
  and actions are expanded, among other string fields. See
  [Environment variable expansion](../configuration.md#environment-variable-expansion).
- **An unset variable is not an error.** If `<NAME>` is not set, `${<NAME>}` stays in the
  value as literal text, passes validation, and becomes the real key in `s3.config`. A
  variable set to an empty string is rejected instead, with `is required`.
- **Only environment variables.** The provisioner has no `<NAME>_FILE` variant and reads no
  secret files. The image is built from `scratch`, so it has no shell either that could
  read a file into a variable. Keys have to arrive as environment variables.
- **No defaults.** `${<NAME>:-<default>}` is not supported. The whole text, including
  `:-<default>`, is taken as the variable name, which never exists, so the placeholder is
  written literally even when `<NAME>` is set.

The keys end up in plain text in `s3.config` anyway, because that is the file SeaweedFS
reads. To limit who can read that file, see
[Restrict who can read s3.config](restrict-who-can-read-s3-config.md).

The examples use the identities `admin` and `reports-app` and the Docker Compose setup from
[Getting started](../getting-started.md#step-2-render-s3config-and-start-seaweedfs). The
output was captured with `seaweedfs-provisioner` 1.0.0 on 2026-11-15.

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
but uses none of these values, so it doesn't need the identity variables. Leave them unset
there rather than set to an empty string, which fails validation.

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
default `optional: false`), so a typo in the Secret name doesn't silently become a literal
placeholder. A variable you forget to list still does. The full layout is in
[Deployment → Kubernetes](../deployment.md#kubernetes).

### 4. Check s3.config for placeholders after the identities phase

The identities phase logs the same lines whether a key was expanded or not. Here
`REPORTS_SECRET_KEY` was not set. Output is trimmed; real lines also carry `time`:

```json
{"level":"INFO","msg":"Writing identity","name":"reports-app"}
{"level":"INFO","msg":"Identity written","name":"reports-app","actions":3}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

The file shows what happened:

```json
    {
      "name": "reports-app",
      "credentials": [
        {
          "accessKey": "reports-key",
          "secretKey": "${REPORTS_SECRET_KEY}"
        }
      ],
```

So search the file for `${` after every identities phase, from a container that mounts the
`s3.config` volume, such as SeaweedFS:

```console
$ docker compose exec seaweedfs grep -n '${' /seaweedfs-config/s3.json
20:          "secretKey": "${REPORTS_SECRET_KEY}"
```

No output and exit code 1 means every placeholder was expanded. On Kubernetes, run the same
`grep` with `kubectl exec` in the SeaweedFS pod (not captured).

## Check that it works

Run the identities phase, then the `grep` from step 4. It prints nothing:

```console
$ docker compose run --rm seaweedfs-identities
$ docker compose exec seaweedfs grep -n '${' /seaweedfs-config/s3.json
$ echo $?
1
```

Then log in with each identity's keys, as in
[Give an application its own credentials](give-an-application-its-own-credentials.md).
`InvalidAccessKeyId` or `SignatureDoesNotMatch` for a key that should work points to a
placeholder or a value that differs between the identities phase and the client.

## Pitfalls

- **A dry run doesn't reveal the placeholder.** `--dry-run` logs only how many credentials
  each identity has, not their values, so a missing variable looks the same as a set one:

  ```json
  {"level":"INFO","msg":"Would write identity","name":"reports-app","credentials":1,"actions":3}
  ```

  Use the `grep` from step 4 instead.
- **A literal placeholder can lock you out.** If the admin key is the placeholder, the
  cluster phase gets `InvalidAccessKeyId` and retries for five minutes. In the test this
  happened during a rotation: the old admin pair was removed and the new one was written as
  `${SEAWEEDFS_ADMIN_ACCESS_KEY_2}`. Fix the variable, run the identities phase, and restart
  SeaweedFS. See [Rotate an access key without downtime](rotate-an-access-key.md).
- **`${<NAME>:-<default>}` is written as is.** With `secret_key: ${REPORTS_SECRET_KEY:-changeme}`
  and `REPORTS_SECRET_KEY` set, `s3.config` still held
  `"secretKey": "${REPORTS_SECRET_KEY:-changeme}"`. Use plain `${<NAME>}`.
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
