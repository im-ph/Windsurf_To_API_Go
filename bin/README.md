# bin/ — bundled runtime artifacts

## `language_server_linux_x64`

Windsurf's official language server binary. The Go service spawns it as a
subprocess and talks to it over gRPC (h2c) on `127.0.0.1:42100+`.

| Field | Value |
|---|---|
| Source | `https://windsurf-stable.codeiumdata.com/linux-x64/stable/abcd9c8664da5af505557f3b327b5537400635f2/Windsurf-linux-x64-2.0.61.tar.gz` |
| Windsurf productVersion | 1.110.1 |
| Windsurf build | 2.0.61 |
| Upstream path inside tarball | `Windsurf/resources/app/extensions/windsurf/bin/language_server_linux_x64` |
| Arch | ELF 64-bit LSB pie, x86-64, stripped Go binary |
| Size | ~177 MB |
| SHA256 | `b0d7460cf7fa4337aedbf873b02862c9fa1b82e99ff1989ad43b9062ff95ce76` |

## Why it lives here

Packaging it alongside the Go binary means a single `scp -r go/` deploys
everything — no separate `/opt/windsurf/` copy step. Config auto-resolves
the path:

1. `$LS_BINARY_PATH` env override (if set)
2. `<exe dir>/bin/language_server_linux_x64` ← **this file**
3. `/opt/windsurf/language_server_linux_x64` (legacy)

## Deploy

```bash
# One-liner from dev box
scp -r e:/WindsurfAPI/go/ root@YOUR_HOST:/opt/windsurfapi/

# On the Linux host
cd /opt/windsurfapi
chmod +x bin/language_server_linux_x64 windsurfapi
./windsurfapi
```

## Windows note

The binary is Linux-only. On Windows the Go service still starts up and
serves `/health` / `/v1/models` / `/dashboard` for non-chat paths — see
[../README.md](../README.md).

## Refreshing

When Windsurf bumps `productVersion`, re-download from the Linux stable
channel manifest:

`https://windsurf-stable.codeium.com/api/update/linux-x64/stable/latest`

Then extract `Windsurf/resources/app/extensions/windsurf/bin/language_server_linux_x64`
from the tarball into this directory. No Go rebuild needed.
