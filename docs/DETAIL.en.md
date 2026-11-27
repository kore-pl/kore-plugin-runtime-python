# Python Runtime Plugin (runtime-python)

## What it does

Detects installed Python on the host (CPython / PyPy, including pyenv and conda environments), reports interpreter details, scans Python projects and runs health checks, creates virtual environments and installs packages; since 0.4.0 it also supports default-version pinning and a minimal single-process daemon (start / stop / restart / status / log tail).
It ships no python binary, adds no resident middleware and does no container orchestration; the daemon has no supervisor process; guarded apps run detached via setsid and all state is on disk.

## Protocol and compatibility

Protocol `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=basic`, `requires_kore >=0.1.0 <0.2.0`.
Plugin capabilities `system:admin` + `proc:write`; of the 20 method-level capabilities, read-only probes / lists / logs are `system:read`, venv / pip / extension install-remove / version pinning / daemon config are `system:admin`, daemon start / stop / restart are `proc:write`.

## Methods

- `versions` / `info` / `projects` / `doctor` / `paths`: discovery and details, project scan (line-level TOML extraction, no third-party library), health check and path resolution; no Python installed yields an install hint.
- `use {version?}`: pin the default version (zero arguments clear it) without changing the host PATH.
- `venv.create` / `venv.list` / `pip.install` / `pip.list`: create virtual environments, list them, install packages into them and list installed packages.
- `ext.install` / `ext.list` / `ext.remove` (since 0.5.0): isolated extension management; packages are flattened into `<state>/global/py_ext` via fixed-argv `python3 -m pip install --target`, never writing host site-packages; removal uses controlled file deletion inside the isolated prefix (pip uninstall has no `--target`), and listing reads only `*.dist-info/METADATA`. Coexists with venv/pip without interference.
- `guard.add` / `guard.list` / `guard.start` / `guard.stop` / `guard.restart` / `guard.remove` / `guard.logs`: local single-process daemon with idempotent start and SIGTERM-then-SIGKILL stop.

## Install and verification

`make check` is the local gate (gofmt, go vet, go test -timeout 200s); `make dist` produces a bit-for-bit reproducible `dist/runtime-python-0.5.0.tar.gz`.
Report vulnerabilities privately per SECURITY.md; MIT license; contributions: CONTRIBUTING.md.