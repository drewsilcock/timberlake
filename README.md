# Timberlake

Timberlake is a tool for efficient, parallelised, resumable file syncing to
S3-compatible object stores such as Amazon S3 and Ceph RGW. It walks a local
directory and uploads its contents to a bucket, skipping files that are already
present with a matching size, and renders live progress in a terminal UI.

## Features

- **Parallel uploads** — configurable worker pool (default 16) with concurrent
  multipart uploads for large files.
- **Resumable** — objects already present at the destination with a matching
  size are skipped, so re-running after an interruption only transfers what's
  missing.
- **S3-compatible** — works against Amazon S3, Ceph RGW, and other
  S3-compatible endpoints, with path-style addressing for self-hosted gateways.
- **`s3cmd` config aware** — reads credentials and endpoint from `~/.s3cfg`
  (or `$S3CMD_CONFIG`) when present, so it slots into existing setups.
- **Live TUI** — a Bubble Tea interface showing per-worker progress, totals, and
  a running summary, with pause/resume support.
- **Dry-run & verify** — preview what would be uploaded, or check remote
  size/existence without writing anything.

## Installation

Requires [Go](https://go.dev/dl/) 1.26+ and, optionally,
[Task](https://taskfile.dev) for the convenience targets.

```sh
# Build with Task
task build

# ...or with the Go toolchain directly
go build -o timberlake main.go
```

This produces a `timberlake` binary in the working directory.

## Usage

```
timberlake [options] SOURCE_DIR s3://BUCKET/PREFIX [JOBS]
```

`SOURCE_DIR` is the local directory to sync, and `s3://BUCKET/PREFIX` is the
destination. An optional positional `JOBS` argument overrides the worker count.

```sh
# Upload a local scan directory into a bucket prefix, using 24 workers
timberlake /data/scan s3://photogrammetry/scans/site-001 24
```

### Options

| Flag | Default | Description |
| --- | --- | --- |
| `-s3cfg` | `$S3CMD_CONFIG` or `~/.s3cfg` | Path to an `s3cmd` config file |
| `-j`, `-jobs` | `16` | Number of parallel worker jobs |
| `-part-size` | `256` | Multipart upload chunk size, in MiB |
| `-endpoint-url` | `$AWS_ENDPOINT_URL` | S3 / Ceph RGW endpoint URL (e.g. `http://rgw.local:8080`) |
| `-access-key` | `$AWS_ACCESS_KEY_ID` | S3 access key ID |
| `-secret-key` | `$AWS_SECRET_ACCESS_KEY` | S3 secret access key |
| `-no-ssl` | `false` | Disable HTTPS for the S3 endpoint |
| `-dry-run` | `false` | Scan and report without uploading |
| `-verify-only` | `false` | Non-writing size/existence check against the destination |

### Configuration & credentials

Credentials and the endpoint are resolved in the following order, with earlier
sources taking precedence:

1. Command-line flags (`-access-key`, `-secret-key`, `-endpoint-url`).
2. An `s3cmd` config file (`-s3cfg`, defaulting to `$S3CMD_CONFIG` then
   `~/.s3cfg`), from which `access_key`, `secret_key`, `host_base`, and
   `use_https` are read.
3. The `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_ENDPOINT_URL`
   environment variables.

### Interactive controls

While a sync is running:

| Key | Action |
| --- | --- |
| `p` / `Space` | Pause / resume |
| `↑` / `↓` (or `k` / `j`) | Scroll the worker list |
| `q` / `Ctrl+C` | Quit |

## Development

```sh
task fmt     # go fmt ./...
task vet     # go vet ./...
task test    # go test -v ./...
task check   # fmt + vet + test
task clean   # remove built binaries
```

Linting uses [golangci-lint](https://golangci-lint.run) (v2) via `.golangci.yml`:

```sh
golangci-lint run ./...
```

Build, tests, and lint also run in CI on every push and pull request against
`main` (see `.github/workflows/ci.yml`).

### Diagnostics

Two standalone helper commands live alongside the app for debugging transfers
against an endpoint (both read credentials/endpoint from the environment or an
AWS profile, and clean up any objects they create):

```sh
# Reproduce a single multipart upload with server-response logging
go run ./s3diag -bucket my-bucket -size 200 -part-size 16

# Measure upload/download throughput and latency across concurrency levels
go run ./speedtest -bucket my-bucket -size 256 -concurrency 1,4,8,16
```

### Errors, cancellation & progress

- **Errors** — if any file fails, the run finishes as *"Sync completed with
  errors"* and the individual failures are written to a
  `timberlake-errors-<timestamp>.log` file in the working directory.
- **Cancellation** — quitting with `q`/`Ctrl+C` before the sync finishes reports
  *"Sync cancelled"*, not success.
- **Speed/ETA** reflect a rolling average of bytes actually uploaded (not local
  read-ahead), so they stay meaningful on slow links. Note that upload
  throughput to high-latency endpoints improves substantially with more
  parallelism, which is why the default part size is a modest 16 MiB.

## Why is it called Timberlake?

Because it keeps your files NSYNC.

## Legal notice

The trademark "NSYNC" does not belong to me. This repository and the code
contained therein is neither endorsed by nor associated with NSYNC or any of its
members (yet, HMU Justin).
