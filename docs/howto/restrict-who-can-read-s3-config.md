# Restrict who can read s3.config

After this, the `s3.config` file the provisioner writes is readable by SeaweedFS but no
longer by every user on the system.

## When you need this

`s3.config` holds every identity's secret key in plaintext, and the provisioner writes it
with mode `0644` by default: anyone who can see the file can read it. That matters when the
volume is shared with other containers, mounted from a host directory, or backed up
elsewhere. You want the file to stay readable for SeaweedFS and nobody else.

## What you need to know first

- **SeaweedFS reads the file as `seaweed`, not as root.** The entrypoint of the official
  image starts as root and switches to the unprivileged user `seaweed` before it starts the
  server. In `chrislusf/seaweedfs:latest` (SeaweedFS 4.53, checked on 2026-11-15) that user
  has user ID (UID) 1000 and group ID (GID) 1000.
- **The provisioner writes the file as root.** Its image sets no user, so the file belongs to
  UID 0 and to the provisioner's group. With `0600`, `seaweed` can't read it and SeaweedFS
  refuses to start. See
  [s3.config file permissions](../deployment.md#s3config-file-permissions).
- **The fix is a shared group.** Run the identities phase with group 1000. The file then
  belongs to `root:seaweed`, and mode `0640` lets the owner write and the group read, while
  everyone else gets nothing. `SEAWEEDFS_S3_CONFIG_MODE` sets the mode; see
  [Environment variables](../deployment.md#environment-variables).

## Steps

### 1. Check the seaweed user in your SeaweedFS image

The IDs belong to the SeaweedFS image, not to the provisioner, so check them for the image
and tag you run:

```console
$ docker run --rm --entrypoint id chrislusf/seaweedfs:latest seaweed
uid=1000(seaweed) gid=1000(seaweed) groups=1000(seaweed),1000(seaweed)
```

Use the `gid` value in the next step. The examples assume 1000.

### 2. Run the identities phase with that group and a tighter mode

In the Compose file from [Getting started](../getting-started.md#step-2-render-s3config-and-start-seaweedfs),
give the identities service the user `0:1000` (root, group 1000) and set the mode. Leave the
cluster-phase service alone; it never touches the file.

```yaml
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    user: "0:1000"                     # root, with the seaweed group of the SeaweedFS image
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_S3_CONFIG_MODE: "0640"
      # … access keys as before
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfsconfig:/seaweedfs-config
```

Keep the user ID at 0. The provisioner writes a temporary file next to `s3.json` and renames
it, so it needs write access to the directory, and a fresh Docker volume's directory belongs
to root.

Quote the mode in YAML. `"0640"` and `"640"` both work; the value is read as an octal number.

### 3. Re-render the file and start SeaweedFS

The provisioner sets owner and mode on every write, so a file already written with `0644`
is fixed on the next run:

```console
$ docker compose up -d
```

The identities phase logs the mode it uses. Output is trimmed; real lines also carry `time`:

```json
{"level":"INFO","msg":"Using s3.config for identities","path":"/seaweedfs-config/s3.json","mode":"0640"}
```

## Check that it works

Look at the file from inside the SeaweedFS container:

```console
$ docker compose exec seaweedfs ls -l /seaweedfs-config/
total 4
-rw-r-----    1 root     seaweed        796 Nov 15 16:50 s3.json
```

SeaweedFS runs as `seaweed`, and its container is healthy:

```console
$ docker compose exec seaweedfs ps -o user,comm
USER     COMMAND
seaweed  weed
root     ps
$ docker compose ps -a --format '{{.Service}} {{.State}} {{.Status}}'
seaweedfs running Up 5 seconds (healthy)
seaweedfs-identities exited Exited (0) 6 seconds ago
```

The cluster phase and the admin identity work as before. The Compose file publishes port
8333:

```console
$ AWS_ACCESS_KEY_ID=<admin-access-key> AWS_SECRET_ACCESS_KEY=<admin-secret-key> AWS_DEFAULT_REGION=us-east-1 \
    aws --endpoint-url http://localhost:8333 s3 ls
2026-11-15 16:51:07 backups
2026-11-15 16:51:07 documents
```

A user outside the group can't read the file any more. Here a container running as user
`nobody` mounts the same volume (with the default Compose project name, the volume is called
`<project>_seaweedfsconfig`):

```console
$ docker run --rm --user 65534:65534 -v <project>_seaweedfsconfig:/c:ro \
    --entrypoint cat chrislusf/seaweedfs:latest /c/s3.json
cat: can't open '/c/s3.json': Permission denied
```

These outputs were captured with Docker Compose on 2026-11-15. The Kubernetes equivalent,
running the identities init container with `runAsUser: 0` and `runAsGroup: 1000` in its
`securityContext`, was not tested.

## Pitfalls

- **SeaweedFS exits with code 255 after you tighten the mode.** The file isn't readable by
  `seaweed`, usually because the mode is `0600` or the identities phase didn't run with the
  `seaweed` group. `docker compose logs seaweedfs` shows:

  ```text
  W1115 16:50:42.818981 auth_credentials.go:663 fail to read /seaweedfs-config/s3.json : open /seaweedfs-config/s3.json: permission denied
  F1115 16:50:42.818997 auth_credentials.go:389 fail to load config file /seaweedfs-config/s3.json: fail to read /seaweedfs-config/s3.json : open /seaweedfs-config/s3.json: permission denied
  ```

  Check owner and group with `ls -l` as above.
- **Running the identities phase as `1000:1000` fails.** The fresh volume's directory belongs
  to root, so the provisioner can't create its temporary file:
  `provisioning identity "admin": writing identity: creating temp file in "/seaweedfs-config": open /seaweedfs-config/s3.json.tmp<random>: permission denied`.
- **`0o640` is rejected.** The mode is parsed as a plain octal number:
  `parsing SEAWEEDFS_S3_CONFIG_MODE "0o640" as an octal file mode: strconv.ParseUint: parsing "0o640": invalid syntax`.
  The run exits with code 1 before writing anything.
- **A different SeaweedFS image or tag can use a different GID.** `latest` isn't pinned, so
  repeat step 1 when you change the image, and after upgrades.
- **Group members can still read the keys.** `0640` keeps out other users, not the
  `seaweed` group or root. Anyone who can read the Docker volume directly on the host, or
  `docker exec` into a container, still gets the secrets. To keep keys out of the config file
  itself, see [Keep access keys out of the config file](keep-access-keys-out-of-the-config.md).

## Further reading

- [s3.config file permissions](../deployment.md#s3config-file-permissions) in the deployment
  docs.
- [Why identities are a file](../concepts.md#why-identities-are-a-file).
- [Set up the admin identity the provisioner logs in with](set-up-the-admin-identity.md).
- The SeaweedFS image's entrypoint, which switches to `seaweed`:
  `docker run --rm --entrypoint cat chrislusf/seaweedfs:latest /entrypoint.sh`.
