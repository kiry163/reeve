# reeve

**A personal AI assistant orchestrator that runs on your own machine.** The chat layer talks, the supervisor dispatches, executors do the work. One Go binary serves both the web console and the Android client; sessions, logs and keys stay on your machine (conversation content is sent to whichever model provider you configure).

[![CI](https://github.com/kiry163/reeve/actions/workflows/ci.yml/badge.svg)](https://github.com/kiry163/reeve/actions/workflows/ci.yml)
[![Release](https://github.com/kiry163/reeve/actions/workflows/release.yml/badge.svg)](https://github.com/kiry163/reeve/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](go.mod)

[中文](README.md)

## What it is

- **Two layers, one rule**: the chat layer (a fast model) understands, checks facts, dispatches and reports — it has **no execution rights**. A pure-Go supervisor queues work and hands it to an executor that can read/write files and run commands.
- **Dangerous work needs your click**: the default approval policy is `untrusted` — reads pass, writes and commands raise a card. Each approval is bound to that one call and cannot be reused.
- **Events are the truth**: every bot appends to its own `stream.jsonl`; clients resume with SSE + `Last-Event-ID`; in-memory state is rebuilt by replaying events at startup.
- **Two clients, one protocol**: an embedded React console and a native Android app (Kotlin + Compose) over REST writes and SSE reads.
- **Single binary, single port**: HTTP, SSE, the web UI and uploads share one listener. No database, no queue, no web framework.

## Features

| Area | What you get |
|---|---|
| Bots | Up to 32, each with its own workspace, memory, secrets, env vars and model |
| Executors | Built-in agent (any OpenAI-compatible model) by default; `codex` / `pi` / `omp` CLIs as explicit upgrades |
| Approvals | Rules → policy → card; one approval is bound to a single call, reusable for identical calls within the same task |
| Env vars | Instance-level + per-call; readable by the model, writes audited; service install imports PATH/timezone/proxy/TLS from your shell; secrets never enter env |
| Vault | AES-256-GCM at rest; the model only sees placeholders, outputs are redacted |
| Skills | Local `SKILL.md`, indexed into the system prompt, body read on demand |
| Slash commands | Parsed by the server and **never sent to the model**: `/help` `/stop` `/clear` `/model` `/agent` `/env` `/skills` `/context` `/tasks` `/approve` |
| Service | `reeve service install` sets up launchd (macOS) or systemd `--user` (Linux) |

Architecture, event catalogue and design rationale live in [docs/superpowers/specs/2026-11-14-reeve-design.md](docs/superpowers/specs/2026-11-14-reeve-design.md) (Chinese).

## Quick start

Requires **Go 1.22+**; building the web console additionally needs **Node.js 22.18+** (CI uses 24; `npm test` relies on Node's type stripping).

```bash
git clone https://github.com/kiry163/reeve.git
cd reeve

make build        # build the frontend, then bundle everything into bin/reeve
./bin/reeve serve # foreground, listens on 127.0.0.1:18800 by default
```

`serve` prints the console URL (<http://127.0.0.1:18800/>) and tries to open a browser. The first run generates a random initial password and prints it **once** (`~/.reeve/password` stores only a salted hash); **every request requires login, including loopback**. Change it from Settings → Account, or run `reeve passwd` on the server.

Go-only builds compile and run, but the console has no UI:

```bash
go build ./cmd/reeve
```

Then add a model: console → Settings → API channels → OpenAI-compatible Base URL, model name and API key.

### Pair a phone

```bash
./bin/reeve serve -addr 0.0.0.0:18800   # reachable on the LAN (no TLS — trusted networks only)
./bin/reeve pair                        # prints reeve://pair?v=1&host=… (address only)
```

The pair string only carries the address; paste it into the Android client or turn it into a QR code yourself, then enter the server password in the app. See [docs/android.md](docs/android.md).

### Run as a service

```bash
./bin/reeve service install    # launchd / systemd --user, no sudo
./bin/reeve service status
./bin/reeve service uninstall
```

The generated unit sets only `REEVE_HOME` and `PATH` — it does **not** listen on other interfaces. To reach it from a phone, add `REEVE_ADDR` as described in [docs/service.md](docs/service.md).

`service install` initializes the instance `env` file from your current shell when it does not exist yet, importing `PATH`, `TZ`, `LANG`/`LC_ALL`, proxy and TLS certificate settings so tasks keep a usable shell environment after the service restarts. Re-import later with `reeve service env refresh`; variables you added by hand are preserved.

## CLI

| Command | Purpose |
|---|---|
| `reeve` / `reeve serve` | Start HTTP + SSE + web console (`-addr` to change the listener) |
| `reeve pair` | Print the pairing address for phones (no password) |
| `reeve passwd` | Reset the login password; signs out every logged-in device |
| `reeve service install\|uninstall\|status` | Manage the user-level service |
| `reeve service env refresh [--dry-run]` | Import PATH/TZ/proxy/TLS from the current shell into the instance `env` |
| `reeve version` | Version info |

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `REEVE_HOME` | `~/.reeve` | Data dir: `password` (hash), `session.key`, `master.key`, per-bot event logs and workspaces |
| `REEVE_ADDR` | `127.0.0.1:18800` | Listen address (same as `serve -addr`) |
| `REEVE_MASTER_KEY` | `$REEVE_HOME/master.key` | Vault master key, may be injected |
| `WEB_DIR` | embedded | Override the web assets with a directory (development) |

## Development

```bash
make help          # list targets
make build         # frontend + Go → bin/reeve
make test          # Go + web + Android unit tests
make test-go       # Go only
make test-web      # web only
make test-android  # Android unit tests (needs JAVA_HOME / ANDROID_HOME)
make lint          # gofmt check + go vet + web typecheck
make apk           # debug APK
make release VERSION=X.Y.Z  # full release (four platforms + SHA256SUMS), see docs/release.md
```

Android toolchain setup is in [docs/android.md](docs/android.md); CI builds and unit-tests the app on every push.

## Docs

| Doc | Content |
|---|---|
| [docs/android.md](docs/android.md) | Android build, pairing, features, manual verification |
| [docs/android-contract.md](docs/android-contract.md) | Client contract: endpoints, events, state, limits |
| [docs/service.md](docs/service.md) | Running as a service and its known edges |
| [docs/release.md](docs/release.md) | Release script, artifact names, signing |
| [CHANGELOG.md](CHANGELOG.md) | Version history |

## Known limits

- **No sandbox**: executors run as your user and can touch your files; approvals are the only guard. Use an external executor or a container if you need isolation.
- **No TLS**: plain HTTP. Trusted LANs only; use Tailscale or an SSH tunnel across networks.
- **Single user**: no multi-tenancy, registration or per-user isolation. Do not expose it to the internet.
- **Platforms**: service install supports macOS and Linux; on Windows only foreground `serve`.
- **No system push**: Android relies on a foreground service plus local notifications; nothing is delivered after the OS kills the app.
- **Not planned**: MCP, provider fallback chains, migration from older versions, iOS or desktop clients.

## License

[MIT](LICENSE) © 2026 kiry163
