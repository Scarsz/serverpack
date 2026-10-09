# serverpack

Git-managed Minecraft server definitions, built into immutable Docker images.

A server is a directory that mirrors its `/data`. Configs are committed as-is. Every jar (the server, plugins and mods) is a small `.toml` descriptor that says where the jar comes from and pins exactly which file it resolved to. `serverpack` resolves the descriptors, fetches the jars into a content-addressed cache and assembles the server. It then builds an image whose entrypoint injects secrets from the environment and execs Java.

```
servers/plot251/
  server.toml                  # the server jar ([server]), runtime, build, volumes, templates, extends
  server.properties            # ordinary config (may contain ${VAR} placeholders)
  plugins/
    FastAsyncWorldEdit.toml    # descriptor → plugins/FastAsyncWorldEdit.jar
    Internal.toml              # source = "local"
    Internal.jar               # vendored jar (the only kind of jar in Git)
    PlotSquared/config/settings.yml
```

See [`examples/plots`](examples/plots) for a complete, locked repo.

## Quick start

```sh
serverpack init --base common
serverpack init plot251 --extends common --mc-version 1.21.10 --accept-eula
serverpack add base:common hangar ViaVersion/ViaVersion
serverpack add plot251 modrinth fastasyncworldedit --name FastAsyncWorldEdit
serverpack check
serverpack build plot251 --push
```

Migrating an existing server directory:

```sh
serverpack import plot251 /srv/compose/plot251 --xms 128M --xmx 8G \
  --jvm-flag=-Dterminal.jline=false --jvm-flag=-Dterminal.ansi=true
serverpack diff plot251 /srv/compose/plot251        # only runtime state should differ
```

`import` identifies plugin jars on Modrinth by hash and pins them to the exact same file. It matches Paper and Folia server jars against PaperMC builds. Anything it can't identify is vendored as `source = "local"`. Worlds, `backups/` directories and logs become volumes. **Configs are copied verbatim, so review them for secrets before committing.**

To convert a whole compose stack of servers at once, the Claude Code skill in [`skills/serverpack-convert`](skills/serverpack-convert/SKILL.md) inventories the stack and imports every server. It extracts shared bases, templates secrets, maps runtime state to volumes and writes a matching compose file, all without touching the live directories. Install it by copying it into `~/.claude/skills/`.

## Repository layout

```
bases/<name>/      shared layers: a full /data mirror plus server.toml
servers/<name>/    one directory per server
```

There is no repo-level config file. Repo-wide defaults such as the image, registries and JVM flags live in a base. The repo root is the nearest directory containing `servers/` or `bases/` (override with `--root`).

### Inheritance

`extends = ["common", …]` layers bases in order, with the server on top. Bases may extend other bases.

- **Files**: a file at the same path replaces the one below it, whether it's a config or a descriptor.
- **Disabling an inherited jar**: put a descriptor containing only `disabled = true` at the same path. `serverpack remove` does this for you.
- **`[server]`**: the top-most layer that defines it replaces the whole table, and its pin is written back to that layer's file. This lets a base provide a shared server jar.
- **`server.toml` scalars**: the last value wins.
- **Lists**:
  - `jvm_flags`, `args`, `jar_dirs`, `build.images`, `build.tags` and `build.platforms` replace what's below them.
  - `jvm_flags_append`, `images_append` and `tags_append` add to what's below.
  - `volumes.paths` and `templates.include` are merged as a union.

### server.toml

Every server and base has one `server.toml` at its root. It is the only file serverpack treats specially there; it is never copied into the image.

```toml
name    = "plot251"            # default: directory name
extends = ["common"]
# jar_dirs = ["plugins", "mods"] # where descriptors live (default)

[server]                         # the server jar: a descriptor (see below) that produces server.jar
source  = "papermc"
project = "paper"
version = "1.21.10"
# build = "latest"

[server.resolved]                # written by lock/update; commit it
version = "1.21.10-130"
# url, sha256, size, mc_version, loader, compat, spec_hash …

[runtime]
image     = "eclipse-temurin:25"
memory    = { min = "128M", max = "24576M" }
jvm_flags = ["-Dterminal.jline=false"]
args      = []                   # after -jar server.jar
# java    = "java"

[build]
images = ["registry.example.com/plots/{name}", "ghcr.io/scarsz/{name}"]
tags   = ["latest", "{mc_version}", "{git_sha}"]   # every image × every tag
# platforms = ["linux/amd64"]
# serverpack_image = "ghcr.io/scarsz/serverpack:v1.0.0"

[volumes]
paths = ["logs", "plugins/PlotSquared/backups"]  # runtime state, excluded from the image

[templates]
include = ["server.properties", "plugins/**/*.yml"]
```

Placeholders in `images` and `tags`:
- `{name}`
- `{mc_version}`
- `{git_sha}`: the short SHA, with `-dirty` appended when the tree has uncommitted changes
- `{date}`: `YYYYMMDD`, UTC

## Descriptors

Any `*.toml` file *directly* inside a jar directory is a descriptor. Files in subdirectories, such as plugin data folders, are ordinary config. The `[server]` table of `server.toml` is a descriptor too, with the same fields, and produces `server.jar`. A descriptor named `X.toml` produces `X.jar` unless it sets `filename`.

```toml
source  = "modrinth"
project = "viaversion"
version = "latest"     # exact | latest (default) | latest-any
# channel = "beta"     # least stable channel allowed: release (default) | beta | alpha
# mc_version = "1.21.8"  # resolve as if the server ran this version
# loader = "paper"       # resolve for this platform instead of the server's

[resolved]             # written by lock/update; commit it
version    = "5.12.1"
url        = "https://cdn.modrinth.com/…/ViaVersion-5.12.1.jar"
sha256     = "…"
size       = 6503217
mc_version = "1.21.10"
loader     = "paper"
compat     = "yes"     # whether this version declares support for mc_version
spec_hash  = "…"       # detects edits to the fields above
```

`lock` and `update` rewrite only the `[resolved]` table (`[server.resolved]` in `server.toml`). The rest of the file, including comments, is preserved.

### Version selectors

| `version` | Picks |
|---|---|
| `"7.5.6"` | Exactly that version. It is never filtered by compatibility, but `lock` and `check` warn if it doesn't declare support for the server's MC version. |
| `"latest"` (default) | The newest version on the allowed channel that supports the server's MC version and platform. If none does, it fails and names the newest version and what it supports. |
| `"latest-any"` | The newest version on the allowed channel, ignoring MC compatibility. |

- The server's MC version and platform come from the `[server]` table (for example, papermc `paper` 1.21.10 means plugins for paper, spigot or bukkit on 1.21.10). Proxies (Velocity, Waterfall) have no MC version, so only the platform is matched.
- Sources without compatibility metadata (GitHub, Jenkins, Maven, URL) treat `latest` as `latest-any`, with a warning.

### Sources

| Source | Fields |
|---|---|
| `papermc` | Paper, Folia, Velocity and Waterfall from PaperMC Fill: `project`, `version` = MC version or `latest`, `build` = N or `latest` |
| `purpur` | `version` = MC version or `latest`, `build` |
| `fabric` | Fabric server launcher: `version` = MC version, `loader_version`, `installer_version` |
| `modrinth` | `project` = slug or id. Filters by MC version and loader. |
| `hangar` | `project` = `Owner/Slug` or slug. Filters by platform and MC version. |
| `spigot` | `project` = resource id or `name.id`, resolved through Spiget. Premium and externally hosted resources are rejected, and Spiget only serves each resource's latest version. |
| `curseforge` | `project` = slug or id. Needs `CURSEFORGE_API_KEY`. Files whose authors disallow third-party downloads are rejected. |
| `bukkitdev` | BukkitDev through the CurseForge API. Same fields as `curseforge`. |
| `github` | `project` = `owner/repo`, `asset` = glob (default `*.jar`, must match exactly one file), `version` = tag. `GITHUB_TOKEN` is optional. |
| `jenkins` | `url` = job URL, `asset` = glob, `version` = build number or `latest` (last successful build) |
| `maven` | `repo` = URL, `project` = `group:artifact[:classifier]`, `version`. Snapshots are supported. |
| `url` | `url`, optional `sha256`. The hash is pinned at lock time. |
| `local` | A jar committed next to the descriptor. `path` overrides the default `X.jar`. |

## Commands

| Command | |
|---|---|
| `init <name> [--base]` | Scaffold a server or base. |
| `add <target> <source> <ref>` | Write a descriptor and resolve it. Targets are server names or `base:<name>`. |
| `remove <target> <name>` | Delete a descriptor, or write a `disabled` stub if the jar is inherited. |
| `import <name> <dir>` | Bootstrap a server from a live directory. |
| `lock [target…]` | Resolve descriptors that have no pin or whose spec changed. All servers by default. |
| `update [server] [name…] [--dry-run]` | Move floating selectors forward. Run it after bumping the MC version in `[server]`. |
| `check [target…]` | Offline validation: stale or missing pins, MC mismatches, stray jars, files inside volume paths, the EULA. |
| `render <server> <dir>` | Assemble the full `/data` tree locally. |
| `build <server…>` | Render, then `docker buildx build` with every image × tag. Supports `--push`, `--image`, `--tag`, `--platform` and `--dry-run`. |
| `diff <server> <dir>` | Show drift against a live directory. Jars are compared by content, and volume paths and runtime files are ignored. |
| `run` | The container entrypoint. |
| `sources` | List the sources. |

Downloads are cached by sha256 in `$SERVERPACK_CACHE`, or the user cache dir if that's unset (override with `--cache`). Lock pins the hash, so builds are reproducible and can't silently change underneath you.

## The image

```dockerfile
FROM eclipse-temurin:25
COPY --from=ghcr.io/scarsz/serverpack:<version> /serverpack /usr/local/bin/serverpack
COPY jars/   /data/     # server.jar + plugins: their own layer, so config edits rebuild fast
COPY config/ /data/     # overlay + /data/.serverpack/runtime.json
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/serverpack", "run"]
```

At container start, `serverpack run`:
1. renders every templated file from a pristine copy, so restarts pick up changed env vars;
2. creates any volume directories that are missing;
3. execs `java -Xms… -Xmx… <jvm_flags> -jar server.jar <args>`, so Java is PID 1 and receives stop signals directly. Arguments after `run --` are appended.

Template syntax:
- `${NAME}` substitutes the variable.
- `${NAME:-default}` uses the default when the variable is unset or empty.
- `$${` produces a literal `${`.
- If a `${NAME}` has no default and the variable is unset, the container refuses to start and lists every missing variable.

Anything else, such as `%placeholders%`, is left alone. Only files matched by `templates.include` are processed.

Compose then only needs the image, the environment and the volumes. See [`examples/plots/docker-compose.yml`](examples/plots/docker-compose.yml).

## Development

```sh
go test ./...                                    # unit + fixture tests
go test -tags live ./internal/source -run Live   # against the real APIs
```

Pushing a `v*` tag publishes binaries through goreleaser and a multi-arch `ghcr.io/scarsz/serverpack` image.
