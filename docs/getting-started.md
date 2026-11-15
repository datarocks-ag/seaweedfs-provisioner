# Getting started

This guide takes one small project from nothing to a SeaweedFS cluster an application can use:
an admin login, a bucket, an application identity that can only reach its own bucket, a
preview before every change, and an application that starts only after provisioning succeeded.

**The project:** you're building `shop`, a small web shop. The shop stores uploaded product
images in S3 (Amazon Simple Storage Service) buckets served by SeaweedFS. The shop's
application needs its own credentials, and it must not be able to touch the backups that live
next to its uploads. Everything runs in Docker Compose.

You need Docker with Compose v2 and, for the checks, the AWS CLI (`aws`). Each step builds on
the previous one, so keep the two files you create: `config.yaml` and `docker-compose.yaml`.

> **Versions:** this guide uses image tag `1.0`. All tags are listed under
> [Deployment → Image tags](deployment.md#image-tags).

## Step 1: Declare the admin identity

Start with the one thing every setup needs: an identity the provisioner itself can log in with.

```yaml
# config.yaml
identities:
  - name: admin
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions:
      - Admin
```

An *identity* is a name, one or more access key pairs, and a list of *actions* it may perform.
`Admin` allows everything. The keys aren't in the file: `${SEAWEEDFS_ADMIN_ACCESS_KEY}` is
read from the environment when the config is loaded, so the file can be committed and
reviewed.

Why start here? SeaweedFS doesn't accept identities over an API. It reads them from a JSON
file, `s3.config`, **when it boots**. As soon as that file holds any identity, SeaweedFS
rejects anonymous requests, so the provisioner needs an `Admin` identity of its own to create
buckets later. [How it works](concepts.md#why-identities-are-a-file) explains the background.

Now the Compose file. Its first service runs the provisioner in the `identities` phase: it
renders the identities into `s3.config` on a shared volume and exits. It doesn't talk to
SeaweedFS at all, which is why it can run before SeaweedFS exists.

```yaml
# docker-compose.yaml
services:
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_ADMIN_ACCESS_KEY: admin-access-key
      SEAWEEDFS_ADMIN_SECRET_KEY: admin-secret-key
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfs-config:/seaweedfs-config

volumes:
  seaweedfs-config:
```

Run it:

```console
$ docker compose run --rm seaweedfs-identities
{"level":"INFO","msg":"Starting seaweedfs-provisioner","version":"1.0.0"}
{"level":"INFO","msg":"Loading configuration","path":"/config.yaml"}
{"level":"INFO","msg":"Configuration loaded","phase":"identities","buckets":0,"identities":1,"policies":0}
{"level":"INFO","msg":"Using s3.config for identities","path":"/seaweedfs-config/s3.json","mode":"0644"}
{"level":"INFO","msg":"Provisioning identities","count":1}
{"level":"INFO","msg":"Writing identity","name":"admin"}
{"level":"INFO","msg":"Identity written","name":"admin","actions":1}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Log lines are JSON. Real lines also carry a `time` field, which is left out in this guide.

The file is written with mode `0644`, readable by everyone in the container. That's
deliberate: the SeaweedFS image reads it as an unprivileged user.
[s3.config file permissions](deployment.md#s3config-file-permissions) explains the trade-off
and how to tighten it.

## Step 2: Render s3.config and start SeaweedFS

With the identities rendered, SeaweedFS can start from them. Add the `seaweedfs` service and a
data volume:

```yaml
  seaweedfs:
    image: chrislusf/seaweedfs:latest
    command: server -s3 -filer -s3.config=/seaweedfs-config/s3.json
    depends_on:
      seaweedfs-identities:
        condition: service_completed_successfully
    ports:
      - "8333:8333"
    volumes:
      - seaweedfs-data:/data
      - seaweedfs-config:/seaweedfs-config:ro
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider -S http://$$(hostname -i):8333/ 2>&1 | grep -qE 'HTTP/1.[01] (200|403)'"]
      interval: 5s
      timeout: 5s
      retries: 20

volumes:
  seaweedfs-config:
  seaweedfs-data:
```

Three details matter here:

- **`depends_on` with `service_completed_successfully`** makes Compose run
  `seaweedfs-identities` first and start SeaweedFS only if it exited with code `0`. SeaweedFS
  never boots without its `s3.config`.
- **`-s3.config=/seaweedfs-config/s3.json`** points the S3 gateway at the file the
  provisioner wrote. The volume is mounted read-only here; only the provisioner writes to it.
- **The healthcheck** looks odd for a reason. SeaweedFS listens on the container's own IP
  address, not on `localhost`, so it probes `$(hostname -i)`. And with identities in place the
  gateway answers the unauthenticated probe with `403 Forbidden`, so the check accepts `403`
  as well as `200` as "the gateway is up". `$$` is how you write a literal `$` in a Compose
  file.

Start SeaweedFS. Compose runs the identities phase again first, which is harmless: the
provisioner writes the same identity again.

```console
$ docker compose up -d seaweedfs
$ docker compose ps -a --format 'table {{.Service}}\t{{.Status}}'
SERVICE                STATUS
seaweedfs              Up 6 seconds (healthy)
seaweedfs-identities   Exited (0) 6 seconds ago
```

Look at the file SeaweedFS booted from:

```console
$ docker compose exec seaweedfs cat /seaweedfs-config/s3.json
{
  "identities": [
    {
      "name": "admin",
      "credentials": [
        {
          "accessKey": "admin-access-key",
          "secretKey": "admin-secret-key"
        }
      ],
      "actions": [
        "Admin"
      ]
    }
  ]
}
```

The gateway now enforces authentication. The Compose file publishes port 8333, so you can
check from your machine. An anonymous request is refused, the admin keys are accepted (and
there are no buckets yet, so the listing is empty):

```console
$ aws --endpoint-url http://localhost:8333 --no-sign-request s3 ls

aws: [ERROR]: An error occurred (AccessDenied) when calling the ListBuckets operation: Access Denied.

$ AWS_ACCESS_KEY_ID=admin-access-key AWS_SECRET_ACCESS_KEY=admin-secret-key \
  AWS_DEFAULT_REGION=us-east-1 \
  aws --endpoint-url http://localhost:8333 s3 ls
$
```

## Step 3: Create a bucket

Buckets are different from identities: they're created over the S3 API, so they need a
running SeaweedFS. That's the second phase, `cluster`. Add two buckets to the config, one for
the shop's uploads and one for backups:

```yaml
# config.yaml
buckets:
  - name: shop-uploads
  - name: shop-backups

identities:
  - name: admin
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions:
      - Admin
```

Then add a second provisioner service that runs the `cluster` phase once SeaweedFS is
healthy. It logs in with the admin keys from step 1:

```yaml
  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=cluster"]
    depends_on:
      seaweedfs:
        condition: service_healthy
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_ENDPOINT: http://seaweedfs:8333
      SEAWEEDFS_ACCESS_KEY: admin-access-key
      SEAWEEDFS_SECRET_KEY: admin-secret-key
    volumes:
      - ./config.yaml:/config.yaml:ro
```

The `cluster` phase doesn't need `SEAWEEDFS_S3_CONFIG_PATH` or the `${…}` variables for the
identity keys: it never writes `s3.config`. It still loads and validates the whole config,
though, so a typo anywhere in the file stops every phase. Run it:

```console
$ docker compose run --rm seaweedfs-provisioner
{"level":"INFO","msg":"Starting seaweedfs-provisioner","version":"1.0.0"}
{"level":"INFO","msg":"Loading configuration","path":"/config.yaml"}
{"level":"INFO","msg":"Configuration loaded","phase":"cluster","buckets":2,"identities":1,"policies":0}
{"level":"INFO","msg":"Connecting to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
{"level":"INFO","msg":"Connected to SeaweedFS S3","endpoint":"http://seaweedfs:8333"}
{"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
{"level":"INFO","msg":"Creating bucket","bucket":"shop-uploads"}
{"level":"INFO","msg":"Creating bucket","bucket":"shop-backups"}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Now run the same command again:

```console
$ docker compose run --rm seaweedfs-provisioner
...
{"level":"INFO","msg":"Provisioning cluster resources","buckets":2,"policies":0}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

Nothing was created, and nothing failed on the buckets that already exist. The provisioner
checked each bucket, found it in place, and moved on; those checks are logged at `DEBUG`
level, so they only show with `LOG_LEVEL=debug`. This is the property everything else builds
on: **you can run the provisioner on every deploy**.

```console
$ AWS_ACCESS_KEY_ID=admin-access-key AWS_SECRET_ACCESS_KEY=admin-secret-key \
  AWS_DEFAULT_REGION=us-east-1 \
  aws --endpoint-url http://localhost:8333 s3 ls
2026-11-15 16:49:13 shop-backups
2026-11-15 16:49:13 shop-uploads
```

## Step 4: Give your application its own identity

The shop shouldn't use the admin keys. Give it an identity that can read, write and list
`shop-uploads`, and nothing else. Append it to the `identities` list:

```yaml
  - name: shop-app
    credentials:
      - access_key: ${SHOP_APP_ACCESS_KEY}
        secret_key: ${SHOP_APP_SECRET_KEY}
    actions:
      - Read:shop-uploads
      - Write:shop-uploads
      - List:shop-uploads
```

`Read:shop-uploads` is an action scoped to one bucket. A bare `Read` would allow reading
every bucket. [Actions](configuration.md#actions) lists them all.

Identities are rendered by the `identities` phase, so that service needs the new keys. Add
them to its `environment`:

```yaml
      SHOP_APP_ACCESS_KEY: shop-app-access-key
      SHOP_APP_SECRET_KEY: shop-app-secret-key
```

Render the file again:

```console
$ docker compose run --rm seaweedfs-identities
...
{"level":"INFO","msg":"Provisioning identities","count":2}
{"level":"INFO","msg":"Writing identity","name":"admin"}
{"level":"INFO","msg":"Identity written","name":"admin","actions":1}
{"level":"INFO","msg":"Writing identity","name":"shop-app"}
{"level":"INFO","msg":"Identity written","name":"shop-app","actions":3}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

…and try the new keys right away:

```console
$ echo hello > hello.txt
$ AWS_ACCESS_KEY_ID=shop-app-access-key AWS_SECRET_ACCESS_KEY=shop-app-secret-key \
  AWS_DEFAULT_REGION=us-east-1 \
  aws --endpoint-url http://localhost:8333 s3 cp --no-progress hello.txt s3://shop-uploads/hello.txt
upload failed: ./hello.txt to s3://shop-uploads/hello.txt An error occurred (InvalidAccessKeyId) when calling the PutObject operation: The access key ID you provided does not exist in our records.
```

The file is updated, but **SeaweedFS reads `s3.config` only when it boots**. The running
gateway still has the old identities. Restart it:

```console
$ docker compose restart seaweedfs
```

Wait until `docker compose ps` shows `seaweedfs` as `healthy` again, then check what the
shop can do. To keep the commands short, export the shop's keys first:

```console
$ export AWS_ACCESS_KEY_ID=shop-app-access-key AWS_SECRET_ACCESS_KEY=shop-app-secret-key AWS_DEFAULT_REGION=us-east-1

$ aws --endpoint-url http://localhost:8333 s3 cp --no-progress hello.txt s3://shop-uploads/hello.txt
upload: ./hello.txt to s3://shop-uploads/hello.txt

$ aws --endpoint-url http://localhost:8333 s3 ls s3://shop-uploads
2026-11-15 16:49:41          6 hello.txt

$ aws --endpoint-url http://localhost:8333 s3 cp --no-progress hello.txt s3://shop-backups/hello.txt
upload failed: ./hello.txt to s3://shop-backups/hello.txt An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.

$ aws --endpoint-url http://localhost:8333 s3 ls s3://shop-backups

aws: [ERROR]: An error occurred (AccessDenied) when calling the ListObjectsV2 operation: Access Denied.

$ aws --endpoint-url http://localhost:8333 s3 ls
2026-11-15 16:49:13 shop-uploads
```

The shop writes and lists its own bucket, is denied on `shop-backups`, and doesn't even see
`shop-backups` when it lists all buckets. The admin keys still work, because the provisioner
rewrites only the identities in the config and keeps the rest of the file.

A restart means a short interruption for every S3 client. On a live cluster, plan for it:
[Change identities on a running cluster](howto/change-identities-on-a-running-cluster.md)
walks through it, and [Rotate an access key without downtime](howto/rotate-an-access-key.md)
shows how to change keys without locking anyone out.

## Step 5: Preview before you apply

This time, look before you leap. The shop wants versioning on its uploads, and a new bucket
for nightly exports whose objects expire after a week. Change the `buckets` list:

```yaml
buckets:
  - name: shop-uploads
    versioning: true
  - name: shop-backups
  - name: shop-exports
    lifecycle:
      - id: expire-old-exports
        expiration_days: 7
```

`DRY_RUN=true` (or `--dry-run`) makes the provisioner read the live state and **log** each
change instead of making it:

```console
$ docker compose run --rm -e DRY_RUN=true seaweedfs-provisioner
{"level":"INFO","msg":"Starting seaweedfs-provisioner","version":"1.0.0"}
{"level":"INFO","msg":"Dry run: no changes will be applied"}
...
{"level":"INFO","msg":"Provisioning cluster resources","buckets":3,"policies":0}
{"level":"INFO","msg":"Would set bucket versioning","bucket":"shop-uploads","current":"","enabled":true}
{"level":"INFO","msg":"Would create bucket","bucket":"shop-exports"}
{"level":"INFO","msg":"Would set bucket lifecycle","bucket":"shop-exports","rules":1}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

`shop-backups` doesn't appear: it is already as configured. `"current":""` means versioning
has never been configured on `shop-uploads`. `shop-exports` doesn't exist yet, so its
lifecycle can't be read; the preview reports what would be set after creating it.

You can preview the identities phase the same way. It doesn't compare with the file, so it
lists every identity, changed or not:

```console
$ docker compose run --rm -e DRY_RUN=true seaweedfs-identities
...
{"level":"INFO","msg":"Would write identity","name":"admin","credentials":1,"actions":1}
{"level":"INFO","msg":"Would write identity","name":"shop-app","credentials":1,"actions":3}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

[How it works](concepts.md#dry-run) has the details. Happy with it? Apply it for real:

```console
$ docker compose run --rm seaweedfs-provisioner
...
{"level":"INFO","msg":"Setting bucket versioning","bucket":"shop-uploads","enabled":true}
{"level":"INFO","msg":"Creating bucket","bucket":"shop-exports"}
{"level":"INFO","msg":"Setting bucket lifecycle","bucket":"shop-exports","rules":1}
{"level":"INFO","msg":"seaweedfs-provisioner finished successfully"}
```

A second dry-run now logs no `Would …` lines at all, and the bucket settings are in place:

```console
$ AWS_ACCESS_KEY_ID=admin-access-key AWS_SECRET_ACCESS_KEY=admin-secret-key \
  AWS_DEFAULT_REGION=us-east-1 \
  aws --endpoint-url http://localhost:8333 s3api get-bucket-versioning --bucket shop-uploads
{
    "Status": "Enabled"
}
```

## Step 6: Start the application after the provisioner

The last piece is ordering. The shop must not start before its bucket exists. Compose
expresses that with `service_completed_successfully` on the `cluster` phase. To keep the
guide runnable, the AWS CLI stands in for the shop: it uploads one file with the shop's keys.
Replace it with your own image.

```yaml
  shop-app:
    image: amazon/aws-cli:latest
    command: ["--endpoint-url", "http://seaweedfs:8333", "s3", "cp", "--no-progress",
              "/etc/os-release", "s3://shop-uploads/os-release.txt"]
    depends_on:
      seaweedfs-provisioner:
        condition: service_completed_successfully
    environment:
      AWS_ACCESS_KEY_ID: shop-app-access-key
      AWS_SECRET_ACCESS_KEY: shop-app-secret-key
      AWS_DEFAULT_REGION: us-east-1
```

To see the whole chain, start from scratch. `down -v` deletes the volumes, including all
data and the rendered `s3.config`:

```console
$ docker compose down -v
$ docker compose up -d
$ docker compose ps -a --format 'table {{.Service}}\t{{.Status}}'
SERVICE                 STATUS
seaweedfs               Up 10 seconds (healthy)
seaweedfs-identities    Exited (0) 11 seconds ago
seaweedfs-provisioner   Exited (0) 5 seconds ago
shop-app                Exited (0) Less than a second ago
$ docker compose logs --no-log-prefix shop-app
upload: ../etc/os-release to s3://shop-uploads/os-release.txt
```

Compose rendered the identities, started SeaweedFS from them, waited for it to be healthy,
created the buckets, and only then started the shop. No restart was needed this time: the
shop's identity was in `s3.config` before SeaweedFS booted.

If any step fails, everything after it stays down. With an invalid bucket name such as
`Shop_Backups` in the config, the very first service stops, because every phase validates the
whole config:

```console
$ docker compose up -d
...
service "seaweedfs-identities" didn't complete successfully: exit 1
$ docker compose ps -a --format 'table {{.Service}}\t{{.Status}}'
SERVICE                 STATUS
seaweedfs               Created
seaweedfs-identities    Exited (1) Less than a second ago
seaweedfs-provisioner   Created
shop-app                Created
$ docker compose logs --no-log-prefix seaweedfs-identities
...
{"level":"ERROR","msg":"Provisioning failed","error":"loading configuration: validating config: buckets[1].name: invalid bucket name \"Shop_Backups\" (must be 3-63 chars, lowercase alphanumeric and hyphens)"}
```

The shop never starts against a half-provisioned cluster, and the provisioner's log tells you
why.

## Where you are now

Your `shop` stack has:

- an admin identity the provisioner logs in with,
- an `s3.config` rendered before SeaweedFS boots, so identities are in place from the start,
- buckets with versioning and an expiration rule,
- an application identity that can reach only its own bucket,
- a provisioning step you can re-run on every deploy and preview with a dry-run,
- an application that starts only after provisioning succeeded.

When you're done experimenting, `docker compose down -v` removes everything.

The final files:

<details>
<summary><code>config.yaml</code></summary>

```yaml
buckets:
  - name: shop-uploads
    versioning: true
  - name: shop-backups
  - name: shop-exports
    lifecycle:
      - id: expire-old-exports
        expiration_days: 7

identities:
  - name: admin
    credentials:
      - access_key: ${SEAWEEDFS_ADMIN_ACCESS_KEY}
        secret_key: ${SEAWEEDFS_ADMIN_SECRET_KEY}
    actions:
      - Admin

  - name: shop-app
    credentials:
      - access_key: ${SHOP_APP_ACCESS_KEY}
        secret_key: ${SHOP_APP_SECRET_KEY}
    actions:
      - Read:shop-uploads
      - Write:shop-uploads
      - List:shop-uploads
```

</details>

<details>
<summary><code>docker-compose.yaml</code></summary>

```yaml
services:
  seaweedfs-identities:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=identities"]
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_CONFIG_PATH: /seaweedfs-config/s3.json
      SEAWEEDFS_ADMIN_ACCESS_KEY: admin-access-key
      SEAWEEDFS_ADMIN_SECRET_KEY: admin-secret-key
      SHOP_APP_ACCESS_KEY: shop-app-access-key
      SHOP_APP_SECRET_KEY: shop-app-secret-key
    volumes:
      - ./config.yaml:/config.yaml:ro
      - seaweedfs-config:/seaweedfs-config

  seaweedfs:
    image: chrislusf/seaweedfs:latest
    command: server -s3 -filer -s3.config=/seaweedfs-config/s3.json
    depends_on:
      seaweedfs-identities:
        condition: service_completed_successfully
    ports:
      - "8333:8333"
    volumes:
      - seaweedfs-data:/data
      - seaweedfs-config:/seaweedfs-config:ro
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider -S http://$$(hostname -i):8333/ 2>&1 | grep -qE 'HTTP/1.[01] (200|403)'"]
      interval: 5s
      timeout: 5s
      retries: 20

  seaweedfs-provisioner:
    image: ghcr.io/datarocks-ag/seaweedfs-provisioner:1.0
    command: ["--phase=cluster"]
    depends_on:
      seaweedfs:
        condition: service_healthy
    environment:
      SEAWEEDFS_CONFIG_PATH: /config.yaml
      SEAWEEDFS_S3_ENDPOINT: http://seaweedfs:8333
      SEAWEEDFS_ACCESS_KEY: admin-access-key
      SEAWEEDFS_SECRET_KEY: admin-secret-key
    volumes:
      - ./config.yaml:/config.yaml:ro

  shop-app:
    image: amazon/aws-cli:latest
    command: ["--endpoint-url", "http://seaweedfs:8333", "s3", "cp", "--no-progress",
              "/etc/os-release", "s3://shop-uploads/os-release.txt"]
    depends_on:
      seaweedfs-provisioner:
        condition: service_completed_successfully
    environment:
      AWS_ACCESS_KEY_ID: shop-app-access-key
      AWS_SECRET_ACCESS_KEY: shop-app-secret-key
      AWS_DEFAULT_REGION: us-east-1

volumes:
  seaweedfs-config:
  seaweedfs-data:
```

</details>

## Next steps

- **Understand what a run does.** [How it works](concepts.md) explains the two phases, what
  the provisioner never deletes, and when to use `strategy: create`.
- **Keep keys out of the Compose file.**
  [Keep access keys out of the config file](howto/keep-access-keys-out-of-the-config.md)
  moves them into secrets.
- **Restrict access further.** [Control bucket access with a policy](howto/control-bucket-access-with-a-policy.md)
  adds a bucket policy, and [Restrict who can read s3.config](howto/restrict-who-can-read-s3-config.md)
  tightens the file mode.
- **Expire more objects.** [Expire objects with lifecycle rules](howto/expire-objects-with-lifecycle-rules.md)
  covers prefixes and disabling rules.
- **Deploy to Kubernetes.** [Deployment](deployment.md#kubernetes) shows the init container
  and Job setups.
