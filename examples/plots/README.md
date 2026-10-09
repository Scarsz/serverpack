# plots

Three PlotSquared plot servers with different plot sizes. They share almost everything, so nearly all of the definition lives in bases and each server is only a few lines.

```
bases/
  common/                      shared by every server in the repo
    server.toml                Java image, base JVM flags, registry + tags, logs volume
    plugins/LuckPerms.toml     modrinth
    plugins/ViaVersion.toml    hangar
    plugins/LuckPerms/config.yml   templated: database credentials come from the environment
  plots/                       extends common; shared by the plot servers
    server.toml                the pinned Paper 1.21.10 [server], --world-container /worlds,
                               PlotSquared backups volume, server.properties template
    plugins/FastAsyncWorldEdit.toml   modrinth
    plugins/ViaBackwards.toml         modrinth
    plugins/EssentialsX.toml          github, exact tag
    server.properties          templated: MOTD and RCON password come from the environment
    eula.txt
servers/
  plot75/    server.toml (Xmx 4G)   plugins/PlotSquared/config/worlds.yml (plot size 75)
  plot251/   server.toml (Xmx 8G)   plugins/PlotSquared/config/worlds.yml (plot size 251)
  plot501/   server.toml (Xmx 16G)  plugins/PlotSquared/config/worlds.yml (plot size 501)
docker-compose.yml             the three services: image, env and mounts only
```

## How the layers combine

Each server has `extends = ["plots"]`, and `plots` has `extends = ["common"]`. They are applied in order `common` → `plots` → server, with later layers winning.

- **Server jar:** `[server]` is defined once, in `bases/plots/server.toml`, and its pin (`[server.resolved]`) lives there too. All three servers run exactly the same Paper build.
- **Plugins and configs:** every file in a base ends up in each server's `/data`. A file at the same path in a server replaces the base's copy. That's how `worlds.yml` differs per server.
- **JVM flags:** `common` sets `jvm_flags`, and `plots` adds to them with `jvm_flags_append`. The servers inherit both.
- **Memory:** set per server. `min` and `max` merge field by field, so a base could set the shared `min = "128M"` and the servers only `max`. The example keeps both in each server so every server's heap is visible in one place.
- **Volumes and templates:** the lists from every layer are merged, so plot251's image excludes both `logs` and `plugins/PlotSquared/backups`, and renders both `server.properties` and the LuckPerms config at start.

`serverpack render plot251 /tmp/plot251` shows the result. plot251's command line comes out as:

```
java -Xms128M -Xmx8G -Dterminal.jline=false -Dterminal.ansi=true \
  -Dviaversion.strict-error-handling1_20_5=false -jar server.jar --world-container /worlds
```

## Environment

The templated configs read these variables when the container starts. The container refuses to start if a variable without a default is missing, and it lists all of them.

| Variable | Used in | Default |
|---|---|---|
| `RCON_PASSWORD` | `server.properties` | required |
| `MOTD` | `server.properties` | `Plots` |
| `LP_DB_PASSWORD` | `plugins/LuckPerms/config.yml` | required |
| `LP_DB_HOST` | `plugins/LuckPerms/config.yml` | `mariadb:3306` |
| `SERVER_NAME` | `plugins/LuckPerms/config.yml` | `global` |

`docker-compose.yml` loads secrets from `.env` and sets a different `MOTD` for each server. To check that an image's templates and environment line up without starting Minecraft:

```sh
docker run --rm --env-file .env registry.example.com/plots/plot251:latest --dry-run
```

## Runtime state

Nothing that changes while a server runs is baked into the image. `docker-compose.yml` mounts it per server, under `./data/<server>/`:

- `logs` → `/data/logs`
- `backups` → `/data/plugins/PlotSquared/backups`
- `worlds` → `/worlds`. Paper is started with `--world-container /worlds`, so worlds live outside `/data`.

## PlotSquared

PlotSquared v7 is a premium Spigot resource and publishes no release jars, so serverpack can't download it. That's why the example contains PlotSquared's configs but no jar. In a real repo, commit the jar next to a local descriptor. Put it in `bases/plots/` so all three servers get it:

```sh
serverpack add base:plots local ./PlotSquared-Bukkit-7.6.0-Premium.jar --name PlotSquared
```

## Common changes

**Add another plot server**

```sh
serverpack init plot1001 --extends plots
```

This creates `servers/plot1001/server.toml`, which inherits everything. Set its `memory`, add its own `plugins/PlotSquared/config/worlds.yml`, then add a service for it to `docker-compose.yml`.

**Update Paper for every plot server**

Change `version` in the `[server]` table of `bases/plots/server.toml`, then:

```sh
serverpack lock      # resolves the new server build once; all three servers share it
serverpack update    # moves "latest" plugins to releases that support the new version
serverpack check
```

Paper sometimes only has beta builds for a brand-new Minecraft version. `lock` then fails and says so, and `channel = "beta"` in `[server]` allows them.

**Add a plugin to one server only**

```sh
serverpack add plot501 modrinth worldguard --name WorldGuard
```

**Build an in-house plugin from source for every plot server**

```sh
serverpack add base:plots build ../plot-tools --task shadowJar --output 'build/libs/*-all.jar'      # Gradle project next to this repo
serverpack add base:plots build https://github.com/me/PlotFork.git --ref main                        # pinned to a commit by lock
```

**Drop an inherited plugin from one server**

```sh
serverpack remove plot75 ViaBackwards    # writes plugins/ViaBackwards.toml with disabled = true
```
