# Shared Backend MinIO Quickstart Guide [![Slack](https://slack.min.io/slack?type=svg)](https://slack.min.io)  [![Docker Pulls](https://img.shields.io/docker/pulls/minio/minio.svg?maxAge=604800)](https://hub.docker.com/r/minio/minio/)

MinIO shared mode lets you use single [NAS](https://en.wikipedia.org/wiki/Network-attached_storage) (like NFS, GlusterFS, and other
distributed filesystems) as the storage backend for multiple MinIO servers. Synchronization among MinIO servers is taken care by design.
Read more about the MinIO shared mode design [here](https://github.com/qkbyte/minio/blob/master/docs/shared-backend/DESIGN.md).

MinIO shared mode is developed to solve several real world use cases, without any special configuration changes. Some of these are

- You have already invested in NAS and would like to use MinIO to add S3 compatibility to your storage tier.
- You need to use NAS with an S3 interface due to your application architecture requirements.
- You expect huge traffic and need a load balanced S3 compatible server, serving files from a single NAS backend.

With a proxy running in front of multiple, shared mode MinIO servers, it is very easy to create a Highly Available, load balanced, AWS S3 compatible storage system.

## Get started

If you're aware of stand-alone MinIO set up, the installation and running remains the same.

## 1. Prerequisites

Install MinIO - [MinIO Quickstart Guide](https://min.io/docs/minio/linux/index.html#quickstart-for-linux).

## 2. Run MinIO on Shared Backend

To run MinIO shared backend instances, you need to start multiple MinIO servers pointing to the same backend storage. We'll see examples on how to do this in the following sections.

- All the nodes running shared MinIO need to have same access key and secret key. To achieve this, we export access key and secret key as environment variables on all the nodes before executing MinIO server command.
- The drive paths below are for demonstration purposes only, you need to replace these with the actual drive paths/folders.

### MinIO shared mode on Ubuntu 16.04 LTS

You'll need the path to the shared volume, e.g. `/path/to/nfs-volume`. Then run the following commands on all the nodes you'd like to launch MinIO.

```sh
export MINIO_ROOT_USER=<ACCESS_KEY>
export MINIO_ROOT_PASSWORD=<SECRET_KEY>
minio gateway nas /path/to/nfs-volume
```

### MinIO shared mode on Windows 2012 Server

You'll need the path to the shared volume, e.g. `\\remote-server\smb`. Then run the following commands on all the nodes you'd like to launch MinIO.

```cmd
set MINIO_ROOT_USER=my-username
set MINIO_ROOT_PASSWORD=my-password
minio.exe gateway nas \\remote-server\smb\export
```

### Windows Tip

If a remote volume, e.g. `\\remote-server\smb` is mounted as a drive, e.g. `M:\`. You can use [`net use`](https://technet.microsoft.com/en-us/library/bb490717.aspx) command to map the drive to a folder.

```cmd
set MINIO_ROOT_USER=my-username
set MINIO_ROOT_PASSWORD=my-password
net use m: \\remote-server\smb\export /P:Yes
minio.exe gateway nas M:\export
```

## 3. Test your setup

To test this setup, access the MinIO server via browser or [`mc`](https://min.io/docs/minio/linux/reference/minio-mc.html#quickstart). You’ll see the uploaded files are accessible from the all the MinIO shared backend endpoints.

## Explore Further

- [Use `mc` with MinIO Server](https://min.io/docs/minio/linux/reference/minio-mc.html)
- [Use `aws-cli` with MinIO Server](https://min.io/docs/minio/linux/integrations/aws-cli-with-minio.html)
- [Use `minio-go` SDK with MinIO Server](https://min.io/docs/minio/linux/developers/go/minio-go.html)
- [The MinIO documentation website](https://min.io/docs/minio/linux/index.html)
