# Offline installation integration test

A real-environment test of fpm's offline contract: package custom Frappe apps on a
machine with network access, then install them into a bench that has **no** network
access, and prove that everything needed was inside the packages.

No mocks. The bench is a [Single Node Frappista](https://github.com/vyogotech/frappista)
image — `docker.io/vyogo/frappe:sne-develop` by default — which carries MariaDB, Redis,
Node, a bench at `/home/frappe/frappe-bench` and a ready site (`dev.localhost`) in one
container. The same image plays both roles: the online builder for `fpm package`
(pip download from PyPI, Frappe's own `bench build`), and the target for `fpm install`,
run with `podman run --network none`. `bench new-site`-equivalent state, `install-app`,
`bench start` and a live reference `bench build` all run for real inside it.

## What it proves

| # | Assertion | Where |
|---|-----------|-------|
| 0 | A non-Frappe checkout is rejected by `fpm package` with exit code 3, before any work | `package-notapp.log` |
| 0 | A `required_apps` entry that is not published fails `fpm package` with exit code 5 | `package-child-unresolved.log` |
| 1 | The install container has no outbound network: `NetworkMode=none`, only loopback, DNS fails, TCP to PyPI/npm fails, `curl` fails | `network-isolation.log` |
| 2 | Installing an app before its required app is refused with exit code 6 and the bench is untouched | `install-child-refused.log` |
| 3 | Every `fpm install` ran pip with `--no-index --find-links <vendored wheels>`; no download lines | `install-*.log` |
| 4 | The bench env holds exactly the versions pinned in `wheels/fpm-lock.txt`; the app imports them on the site | `pip-show.txt`, `pip-freeze.txt`, `demo_table.txt` |
| 5 | `required_apps` were satisfied from the local FPM store; all apps are installed on the site | `list-apps.txt` |
| 6 | `sites/assets/assets.json` + `assets-rtl.json` equal (keys and values) what a live `bench build --apps …` produces for the same apps in the same image, and are `JSON.stringify(obj, null, 4)`-shaped | `offline-assets*.json`, `reference-assets*.json` |
| 7 | `/login` and `/app` reference the apps' hashed bundles through `assets.json`; each bundle and a static image return 200 with the expected content | `http-*.html`, `http-assets.txt` |

The report is written to `.work/results/RESULT.md` after a run.

## Fixture apps (`apps/`)

- `fpm_demo_base` — third-party Python deps (`tabulate`, pure; `msgpack`, binary
  wheel), JS + SCSS bundles, a static image.
- `fpm_demo_child` — `required_apps = ["fpmtest/fpm_demo_base"]`, its own bundles.
- `fpm_demo_plain` — no JS, no Python deps.
- `not_a_frappe_app` — a plain Python package, for the rejection path.

Each is turned into a git checkout on the host before packaging, so packages carry a
real `commit_sha` and `fpm exists --commit` can be exercised.

## Running

```sh
export FPM_OFFLINE_SSH_HOST=varun@192.168.1.111     # a podman host reachable by SSH
test/offline/run.sh all                              # sync → image → package → offline
# or, phase by phase (each re-runnable):
test/offline/run.sh sync      # cross-build fpm, rsync it and the fixtures
test/offline/run.sh image     # pull the frappista image, show its bench
test/offline/run.sh package   # package fixtures inside the image (online)
test/offline/run.sh offline   # --network none container: install + all assertions
test/offline/run.sh verify    # re-run the assertions against the running container
test/offline/run.sh ui        # headless Chromium in the container's netns: screenshots + report
test/offline/run.sh tunnel    # browse the isolated site yourself from the LAN (see below)
test/offline/run.sh clean
```

### Real apps: erpnext + hrms

`test/offline/run.sh real` answers "if hrms depends on erpnext, can fpm pack with all
deps?" with the real apps. Online, inside the image: shallow-fetch erpnext and hrms at
commits contemporary with the image's Frappe (`FPM_OFFLINE_ERPNEXT_REF` /
`FPM_OFFLINE_HRMS_REF`; develop HEAD may use framework APIs the image lacks),
`fpm package erpnext --bench-path …` (stages the checkout into `apps/`, `yarn install`,
Frappe's build incl. the `banking` Vite frontend, wheels vendored for cp3.14/manylinux with
sdist-only packages such as `googlemaps` built as universal wheels), then
`fpm package hrms --with-deps` → `hrms-<ver>-bundle/` holding erpnext + hrms. Offline, in
the running `--network none` container: a fresh site, `fpm install <bundle>` (erpnext
first, then hrms), and assertions that pip ran offline, `required_apps` came from the local
store, nothing was built at install, and both apps' bundles are in `assets.json`.
`FPM_OFFLINE_REAL_REUSE=1` skips the online half when the packages already exist.

### Seeing it in a browser

`ui` runs `docker.io/zenika/alpine-chrome:with-puppeteer` with `--network container:fpm-offline`,
i.e. inside the bench container's network namespace: it reaches `127.0.0.1:8000` and nothing
else. `ui/shot.js` visits `/login`, logs in as Administrator, opens the desk, and writes
`out/ui/ui-*.png` plus `out/ui/ui-report.json` (the markers each app's JS bundle set, the
computed CSS from each app's stylesheet, every `/assets/fpm_demo_*` response, and
`frappe.boot.assets_json` / `frappe.boot.versions`). The fixture bundles inject a visible
banner on every page, so the screenshots show the JS and CSS working, not just loading.

`tunnel` lets you open the site from your own browser without giving the container any
network: `ui/relay.py` forwards a UNIX socket (a bind mount) to `127.0.0.1:8000` from inside
the container's namespace, and a second copy on the host forwards TCP `:8080` to that
socket — UNIX sockets cross network namespaces, TCP does not. Then browse
`http://<host>:8080/login` (Administrator / admin). `clean` stops both relays.

`FPM_OFFLINE_IMAGE` selects another frappista tag (e.g. `docker.io/vyogo/frappe:sne-version-15`);
the wheel target's Python version is read from the image's `env/bin/python`.
Or through Go: `FPM_OFFLINE_SSH_HOST=… go test -tags integration ./test/offline/`.

## Layout on the host (`~/fpm-offline-test`)

```
bin/fpm                 cross-built CLI, mounted at /opt/fpm
apps/                   fixtures; the asset build writes public/dist into them
builder-home/.fpm       the builder's local FPM store (required_apps resolve against it)
out/                    packages, logs, manifests, RESULT.md
remote.sh               host-side phases
```

Note: the image's `Procfile` has a `watch: bench watch` entry; the target container is
started with it removed, because esbuild's watcher would rebuild every app in `apps/` in
development mode and rewrite `assets.json` — exactly what an offline install must not need.
