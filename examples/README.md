# Examples

Each directory here is a complete serverpack repo: a `bases/` and `servers/` tree, already locked, so it renders and builds without running `lock` first.

| Example | Shows |
|---|---|
| [`plots`](plots) | Three plot servers (`plot75`, `plot251`, `plot501`) that share one pinned Paper build, the same plugins and the same configs through two layered bases. Each server sets only its memory and plot size. It also covers templated secrets, runtime-state volumes and a matching `docker-compose.yml`. |

## Using an example

Run serverpack from inside the example directory (or pass `--root examples/<name>` from anywhere):

```sh
cd examples/plots
serverpack check                          # validates every server, offline
serverpack render plot251 /tmp/plot251    # assembles plot251's /data tree locally
serverpack build plot251 --dry-run        # stages the Docker build context and prints the command
```

The first `render` or `build` downloads the pinned jars into your serverpack cache (`$SERVERPACK_CACHE`, or the user cache dir). Each jar is verified against the sha256 in its pin.

Building real images before a serverpack release is published needs a local Linux build of serverpack:

```sh
CGO_ENABLED=0 GOOS=linux go build -o /tmp/serverpack-linux .   # from the repo root
serverpack build plot251 --entrypoint-bin /tmp/serverpack-linux --image local/plot251 --tag dev
```

To experiment, copy an example somewhere else first. `lock`, `update`, `add` and `remove` edit the files in place.
