---
name: serverpack-convert
description: Convert a directory of existing Minecraft servers (typically a docker-compose stack whose services bind-mount ./<server>:/data) into a serverpack repo — one servers/<name>/ per server, shared bases, templated secrets, runtime-state volumes, and a matching compose file. Use when the user is in (or points at) a root directory containing several live server directories and wants them migrated to serverpack.
argument-hint: "[output repo dir] [server names…]"
---

# Convert a multi-server root to serverpack

You are migrating live Minecraft servers into a serverpack repo (format: https://github.com/Scarsz/serverpack, see its README). The starting directory is the **source root**: it contains server directories, usually wired up by a `docker-compose.yml`. The result is a **new repo directory**. The live servers are never changed.

## Hard rules

- **Never modify, move or delete anything in the source root.** Treat it as read-only. All output goes to the output repo. Don't run `docker compose up/down/restart`, and don't stop containers.
- **Never print secret values** in chat or in the report. Write them only to the env file described in step 6. Refer to them by variable name.
- Don't push images or Git commits, and don't touch registries, unless the user asks in this session.
- Ask before choices that lose data or change runtime behavior, such as which state becomes a volume. Batch those questions; don't ask one per server.
- If `serverpack` fails in a way this skill doesn't cover, stop and report the exact error. Don't hand-craft descriptors to work around a resolver error you don't understand.

## 0. Preflight

1. Locate the binary with `serverpack --version`.
   - If it's missing, ask where the serverpack checkout is and build it: `go build -o <somewhere on PATH>/serverpack .`. Release binaries also exist once a `v*` tag is published.
   - On Windows Git Bash, Docker Desktop's CLI may not be on PATH. Add `/c/Program Files/Docker/Docker/resources/bin` to it.
2. Settle the output repo directory.
   - Use the first argument if one was given. Otherwise propose `../serverpack-repo` (a sibling, so it isn't nested inside the bind-mounted stack) and confirm.
   - It must not exist yet, or it must be an existing serverpack repo the user wants to add servers to.
3. Pass `--root <out>` on every serverpack command from here on.

## 1. Inventory

Build a table with one row per candidate server. Don't write anything yet.

**From compose files** (`docker-compose*.yml`, `compose*.yaml` in the source root): a service is a candidate if one of its volumes mounts a host directory at its `working_dir`, usually `./<dir>:/data`. For each candidate, record:

| field | where it comes from |
|---|---|
| name | the service key (sanitize it to `[A-Za-z0-9_.-]`) |
| host dir | the directory mounted at `/data` |
| image | `image:`. This is the Java base image, e.g. `eclipse-temurin:25`. |
| memory | `-Xms` / `-Xmx` in `command`/`entrypoint` |
| jvm flags | every other `-…` token before `-jar` |
| jar | the token after `-jar` (compare it with what `import` detects) |
| args | the tokens after the jar (`--nogui`, `--world-container …`) |
| other mounts | every volume whose container path isn't `/data` or below it (e.g. `/worlds`, `/schematics`) |
| nested mounts | volumes mounted *inside* `/data` (e.g. `…:/data/plugins/PlotSquared/backups`). These become `[volumes] paths`. |
| env_file / environment, networks, labels, restart, container_name, ports | copied to the new compose file as-is |

**Directories without a compose service:** also list immediate subdirectories of the source root that look like servers but aren't referenced by any compose service. Signs of a server are `server.properties`, `velocity.toml`, `bungee`/`waterfall` `config.yml`, or a root `*.jar` together with `eula.txt`. Mark them "unmanaged" and ask whether to include them. They have no command to derive memory or flags from, so ask for those once for the whole group.

**Flag these and exclude them unless the user says otherwise:**
- **Dynamically managed servers.** These are services created by an orchestrator (Pterodactyl, Multipaper, Velocity auto-servers, compose `profiles`, or labels that suggest it), or directories that are clearly templates. Ask if you're unsure.
- **`itzg/minecraft-server` (and similar) images.** These are configured through env vars instead of a java command line. They can be converted:
  - `TYPE`/`VERSION` become `[server]` (e.g. `TYPE=PAPER` gives `source = "papermc"`, `project = "paper"`).
  - `MEMORY`, `INIT_MEMORY` and `MAX_MEMORY` become `memory`.
  - `JVM_OPTS`, `JVM_XX_OPTS` and `JVM_DD_OPTS` become `jvm_flags`.
  - The image tag's Java version becomes `eclipse-temurin:<N>`.
  Confirm this mapping with the user first.
- **Forge/NeoForge servers.** They start from `run.sh` or `@libraries/...` argfiles, not `-jar`. serverpack has no Forge source, so list them as unsupported and skip them.
- **Servers whose `-jar` isn't the jar `import` would pick.** `import` uses `server.jar`, else the largest root jar. If there are several root jars and compose names a smaller one, note it so you can fix `[server]` after import.

Show the inventory table (name, host dir, platform guess, memory, number of flags, other mounts) and get confirmation of the final set before step 2.

## 2. Import every server

For each confirmed server:

```sh
serverpack --root <out> import <name> <source-root>/<host dir> \
  --image <image> --xms <Xms> --xmx <Xmx> \
  --jvm-flag=<flag> …   # one per flag; always use the = form, flags start with "-"
  --arg <arg> …         # one per server arg
```

- Run imports one at a time and keep each report. The report lists:
  - the identified `[server]` jar (e.g. "papermc paper 1.21.10 build 130")
  - plugins matched on Modrinth, pinned to the exact same file
  - vendored jars (`source = "local"`)
  - worlds, skipped files, and suggested volumes
- `import` is read-only on the source. It copies configs, hashes jars into the serverpack cache, and identifies them online (Modrinth by sha1, PaperMC Fill by sha256). Use `--offline` only if network access fails, and say so in the report.
- If the jar in the report differs from compose's `-jar`, fix the `[server]` table in `servers/<name>/server.toml`.

Right after all imports, record a **baseline** for every server:

```sh
serverpack --root <out> check <name>
serverpack --root <out> diff <name> <source-root>/<host dir>  > /tmp/<name>.baseline.diff   # exit 1 just means "differences"
```

The baseline diff should list only runtime state:
- logs, worlds and volumes are already excluded
- databases and nested library jars
- files plugins generate at runtime

Anything else is an import problem. Investigate it before continuing.

## 3. Runtime state → volumes

Everything that used to persist in the bind-mounted directory will now live in the image. It is **lost when the container is recreated** unless it is mounted. Go through each server's import report and baseline diff and sort the stateful paths into these buckets:

| kind | examples | default |
|---|---|---|
| worlds | dirs containing `level.dat` | volume (import already added these) |
| backups | `**/backups/` | volume (import already added these) |
| plugin databases | `*.db`, `*.mv.db`, `*.sqlite`, H2 trace/lock files | volume for the file's **directory**. If that directory also holds configs you want in Git, ask: mount the directory (configs stay live-editable and not in the image) or switch the plugin to the external DB the stack already has. |
| player data outside worlds | `plugins/Essentials/userdata/`, `plugins/*/data/`, `playerdata/` | volume |
| lists changed by in-game commands | `ops.json`, `whitelist.json`, `banned-*.json`, LuckPerms local storage | **ask once for all servers**: keep in Git (in-game changes are lost on recreate, edits go through Git) or make them volumes |
| logs | `logs/` | volume (default from import) |
| existing compose nested mounts | from the inventory | always a volume |

- Volume paths are directories relative to `/data`.
- A single file can be a volume path too, as long as compose bind-mounts that exact file. `serverpack run` skips creating paths that already exist.
- Edit `[volumes] paths` in each `server.toml`.
- Any file in the repo that now falls under a volume path is excluded from the image, and `check` warns about it. Delete those copies from `servers/<name>/` (only in the output repo).

## 4. Shared bases

Look for things that are identical across servers:
- **Plugin descriptors** with the same `source`, `project` and the same pinned `[resolved] sha256` in two or more servers.
- **Config files** with identical content at the same relative path, e.g. `plugins/LuckPerms/config.yml` or `spigot.yml`. Compare with `sha256sum`.
- **`server.toml` settings**: `[runtime] image`, `jvm_flags`, the `[server]` table (only if the version and pin are identical), `[build]` images/tags and `[templates]`.

Propose a base layout. Usually that's one `common` base, plus group bases such as `paper-1.21` or `proxy` when clear clusters exist. Show which servers extend what and what moves. After the user agrees:

1. `serverpack --root <out> init <base> --base`, then move the shared files from each `servers/<name>/…` to `bases/<base>/…` at the same relative path. Delete the server copies.
2. Move shared manifest settings into the base's `server.toml`. Put `extends = ["<base>"]` in each server. Use `jvm_flags_append` / `images_append` for server-specific additions on top of base lists.
3. If a server needs a different version of a shared plugin, keep its own descriptor. Its file overrides the base's at the same path.
4. If a server must *not* get a shared plugin, add `plugins/<Name>.toml` containing only `disabled = true`.
5. **Verify nothing changed.** For every server, `check` must pass and `diff` must match its baseline exactly: `diff <(serverpack … diff …) /tmp/<name>.baseline.diff`. Any difference means the base extraction changed what the image contains. Fix it before moving on.

## 5. Vendored jars

For each `source = "local"` jar, try to find a remote source. Use the jar's `plugin.yml` name and version, then look on:
- Modrinth search
- Hangar: `https://hangar.papermc.io/api/v1/projects?q=<name>`
- the plugin's GitHub releases
- Spigot, through Spiget

If you find a candidate:

```sh
serverpack --root <out> add <server|base:name> <source> <project> --name <Name> --version <exact version> [--asset 'glob'] --force
```

Compare the new `[resolved] sha256` with `sha256sum` of the vendored jar.
- If they match, delete the vendored `.jar`.
- If they don't match, it's a different build. Ask before accepting the version change, or restore `source = "local"`.

Premium resources (Spigot premium, private builds) stay vendored. Say so in the report. Re-run the step 4 baseline comparison afterwards.

## 6. Secrets → templates

Search every config in the output repo for secrets:
- keys matching `password|passwd|secret|token|api[-_]?key|webhook|private[-_]?key|license|auth`
- JDBC/Mongo/Redis URLs that contain credentials
- `rcon.password` in `server.properties`
- Velocity's `forwarding.secret` file (the whole file is the secret)
- `proxies.velocity.secret` in `config/paper-global.yml`
- `settings.bungeecord` setups with IP forwarding secrets
- Discord bot tokens (DiscordSRV `config.yml`, `DiscordSRV/…`)
- Database `password:` keys (LuckPerms, CoreProtect, Plan, …)

For each one:
1. Replace the value with `${UPPER_SNAKE_NAME}`. Name it after the server only when it differs per server, e.g. `PLOTLARGE_RCON_PASSWORD`. For values that are the same everywhere, use a shared name such as `LP_DB_PASSWORD`.
2. Add the file to `[templates] include` in that server's or base's `server.toml`.
3. Write `NAME=value` to `<out>/secrets.env`. Make sure `secrets.env` is git-ignored in `<out>/.gitignore`. If compose already has an `env_file` carrying these values, tell the user which new names must be added to it. Don't edit their production `.env`.

Non-secret values that legitimately differ per environment can use `${NAME:-default}`, so the default keeps working.

Escape any literal `${` that isn't meant as a variable as `$${`. Check files in `templates.include` for those before adding them.

Then run `check` on everything. `diff` will now show the templated files as `?` until their variables are set. Re-run it with the env loaded (`set -a; . <out>/secrets.env; set +a`); it must match the baseline again.

## 7. Build settings and compose

1. Ask once for the registry and image naming, e.g. `registry.example.com/plots/{name}`, and the tags (default `["latest", "{mc_version}", "{git_sha}"]`). Put them in the `common` base's `[build]` section.
2. Write `<out>/docker-compose.yml`. Never overwrite the source compose. For every converted service, derive the entry from the original and change it as follows:
   - **image:** set it to the built image ref.
   - **remove:** `command`, `entrypoint`, `working_dir` and the `./<dir>:/data` mount.
   - **keep:** `container_name`, `env_file`, `environment`, `restart`, `networks`, `labels`, `ports`, and every other mount (`/worlds`, `/schematics`, …).
   - **add:** one bind mount per `[volumes] paths` entry that points at the **existing** host location, so state stays where it is: `<source-root>/<dir>/<path>:/data/<path>`. Use the same relative style as the original compose. Keep existing nested mounts unchanged.
   - **env_file:** add `secrets.env` (or tell the user which variables to add to their existing env file).
3. Copy over top-level `networks:`/`volumes:` definitions that the converted services use.

## 8. Build and smoke-test (ask first)

If Docker is available and the user agrees:

```sh
# Until ghcr.io/scarsz/serverpack:<version> exists, embed a local linux build:
CGO_ENABLED=0 GOOS=linux go build -o /tmp/serverpack-linux <serverpack checkout>
serverpack --root <out> build <name> --entrypoint-bin /tmp/serverpack-linux --image serverpack-test/<name> --tag convert
# Validates templates and volume layout without starting Minecraft:
docker run --rm --env-file <out>/secrets.env serverpack-test/<name>:convert --dry-run
```

- The dry run prints the java command, or lists every missing variable.
- Only boot a full server if the user asks. If they do, never mount the production worlds or volumes into the test container. Use `--network none` or an isolated network so it can't join production databases or proxies, and expect plugins that need those to log connection errors.
- Remove the test images afterwards (`docker rmi`).

## 9. Git

Offer to run `git init` in `<out>` and make an initial commit. Before committing:
- `.gitignore` must contain `/.serverpack/` and `secrets.env`.
- `git status` must show no secrets file and no world, database or log data.

Don't add a remote and don't push.

## 10. Report

Finish with:

1. **A table with one row per server:** platform and MC version, the `[server]` source, plugin counts (pinned remote / vendored local), the bases it extends, the number of volume paths, templated variable *names*, and the result of `check` and the baseline `diff`.
2. **Skipped servers** and why (dynamic, Forge, declined).
3. **Remaining warnings** from `check`, vendored jars still awaiting a source, and any state decisions the user should double-check.
4. **Cutover steps for the user to do themselves, one server at a time.** Don't perform them.
   1. Build and push.
   2. Add the new env vars.
   3. Stop the old service.
   4. Swap in the new service definition.
   5. Start it and watch the logs.
   6. Keep the old directory until the new container has run cleanly. The volumes point into it.