# Timberlake

Timberlake is a tool for efficient, parallelised, resumable file syncing between
local filesystems, S3-compatible object stores (Amazon S3, Ceph RGW), and SFTP
servers. Any supported source can be synced to any supported destination; it
skips files already present at full size and renders live progress in a terminal
UI.

## Status

I'm using this for relatively big (few TB) transfers and it seems reliable so
far. The code is fairly vibey – if it turns out to be useful longer-term, I'll
probably do some re-writing.

Source/destination are now generic `Source`/`Destination` interfaces
(`transfer/`), with local, S3, and SFTP backends and per-file resume. I've
tested the S3 destination fairly thoroughly, the others are still a bit
experimental.

## Features

- **Pluggable backends** — local filesystem, `s3://` object stores, and
  `sftp://` servers, mixable in either direction (local→S3, S3→local,
  local→SFTP, …). Each backend is a small `Source`/`Destination` pair under
  `transfer/`.
- **Parallel transfers** — configurable worker pool (default 16) with concurrent
  multipart uploads for large files.
- **Resumable at two levels** — whole files already present at full size are
  skipped; and an *interrupted single file* resumes from its last committed
  chunk. S3 verifies each existing multipart part by checksum and re-uploads only
  the missing or altered ones; SFTP verifies the partial's bytes against the
  source before appending, and re-uploads from scratch on any mismatch.
- **S3-compatible** — path-style addressing and checksum handling tuned for
  self-hosted gateways like Ceph RGW.
- **`s3cmd` config aware** — reads credentials and endpoint from `~/.s3cfg`
  (or `$S3CMD_CONFIG`) when present.
- **Live TUI** — per-worker bars (committed / in-flight / read-ahead), totals,
  rolling speed, and a running summary, with pause/resume support. Navigate
  workers with `j`/`k` and press Space to zoom into one.
- **Watch from your phone** — `--web` serves a read-only progress page over
  WebSockets and shows a QR code in the TUI. LAN-only by default; press `w` to
  open a public Cloudflare quick tunnel (needs `cloudflared` on PATH).

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
timberlake [options] SOURCE DEST [JOBS]
```

`SOURCE` and `DEST` may each be one of:

| Form | Backend |
| --- | --- |
| `/path/to/dir` (or `file:///path`) | local filesystem |
| `s3://bucket/prefix` | S3 / Ceph RGW |
| `sftp://[user@]host[:port]/path` | SFTP (over an existing SSH server) |

An optional positional `JOBS` argument overrides the worker count.

```sh
# Local directory → S3 bucket prefix, 24 workers
timberlake /data/scan s3://my-bucket/scans/site-001 24

# Local directory → SFTP server (key/agent auth, or -sftp-password)
timberlake /data/scan sftp://user@host/backup/scan

# Pull an S3 prefix back down to local disk
timberlake s3://my-bucket/scans/site-001 /restore/site-001
```

SFTP connects to an existing SSH server's `sftp` subsystem (no separate daemon);
auth uses your SSH agent / default keys, or `-sftp-key` / `-sftp-password`. Host
keys are checked against `~/.ssh/known_hosts` unless `-sftp-insecure` is set.

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
| `p` | Pause / resume |
| `↑` / `↓` (or `k` / `j`) | Move through the worker list |
| `Space` / `Enter` | Zoom the selected worker (Esc to go back) |
| `r` | Show/hide the QR share panel (with `--web`) |
| `w` | Start/stop the public Cloudflare quick tunnel (with `--web`) |
| `q` / `Ctrl+C` | Quit |

### Web progress page

`--web` (default address `:8765`) serves a self-contained, read-only page that
streams live progress over a WebSocket, plus a QR code in the TUI to open it on
your phone. The page lives under an unguessable token path, and every other path
returns 404.

By default it is reachable only from your LAN. Pressing `w` starts a Cloudflare
quick tunnel for a public link — no account needed, but the URL changes every
run, Cloudflare offers no uptime guarantee, and the link (though read-only)
exposes file names and endpoints to anyone who has it. WebSockets are used
rather than SSE because quick tunnels buffer `text/event-stream` responses.

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

### Testing

Tests are **end-to-end per backend** (real byte transfers verified by checksum),
not mocks:

- `task test` — local↔local and local↔SFTP (via an in-process SSH/SFTP server)
  round-trips and resume. No external services; always runs.
- `task test-integration` — the above plus the S3 backend (round-trips, and
  per-file resume incl. interrupt-and-resume and checksum-guarded re-upload of a
  corrupt part) against a throwaway MinIO container.

The S3 tests skip themselves unless `TL_S3_ENDPOINT` and `TL_S3_BUCKET` (plus
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`) are set, so they can also point at a
real Ceph endpoint. Shared helpers live in `transfer/transfertest`.

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

## I am very boring and want a file syncing TUI with absolutely no references to popular 90s boy band NSYNC

Just use `--out-of-sync` to enable ultra-boring mode. It's your choice, as long as you realise there are ZERO NSYNC trivia facts in the Out Of Sync version.

## Legal notice

The trademark "NSYNC" does not belong to me. This repository and the code
contained therein is neither endorsed by nor associated with NSYNC or any of its
members (yet, HMU Justin).
