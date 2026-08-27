# Frappe Package Manager (FPM)

[![CI](https://github.com/vyogotech/fpm/actions/workflows/ci.yml/badge.svg)](https://github.com/vyogotech/fpm/actions/workflows/ci.yml)
[![Release](https://github.com/vyogotech/fpm/actions/workflows/release.yml/badge.svg)](https://github.com/vyogotech/fpm/actions/workflows/release.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/vyogotech/fpm)](https://golang.org/doc/devel/release.html)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

FPM is a command-line interface and package management system for Frappe applications, providing package creation, installation, and repository management to streamline Frappe app deployment.

## 🎯 Overview

FPM transforms Frappe application deployment from a Git-centric model into an enterprise-ready package management system, similar to npm, Maven, or PyPI but designed specifically for the Frappe ecosystem.

### Key Features

- 📦 **Package Management**: Create, publish, and install `.fpm` packages
- 🗄️ **Repository System**: Host your own package repositories with authentication
- 🔍 **Package Discovery**: Search across multiple repositories with priority-based resolution
- 🔐 **Enterprise Security**: HTTP Basic Auth, checksums, and integrity verification
- 🚀 **Offline Deployments**: Reproducible, version-specific deployments without Git
- 🌐 **Multi-Platform**: Linux, macOS, Windows support (AMD64 & ARM64)

## 🚀 Quick Start

### Install FPM CLI

Download the latest release for your platform from [Releases](https://github.com/vyogotech/fpm/releases):

```bash
# Automated install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/vyogotech/fpm/main/install.sh | bash

# Verify installation
fpm --version
fpm --help
```

### Basic Usage

```bash
# Package a Frappe app
cd /path/to/your-frappe-app
fpm package --version 1.0.0 --org myorg

# Add a repository
fpm repo add company-repo https://fpm.company.com --priority 10

# Publish to repository
fpm publish myorg/my-app==1.0.0 --repo company-repo

# Search for packages
fpm search myorg/my-app

# Install a package
fpm install myorg/my-app==1.0.0 --bench-path /path/to/bench
```

## 📖 Documentation

### For Users

- **[Quick Start Guide](QUICK_START.md)** - Get started in 5 minutes
- **[CLI Reference](#cli-commands)** - Complete command documentation
- **[Vision Document](VISION.md)** - Project goals and architecture

### For Repository Administrators

- **[Repository Setup Guide](fpm-repo-README.md)** - Deploy your own FPM repository
- **[Deployment Test Results](DEPLOYMENT_TEST_RESULTS.md)** - Known issues and solutions

### For Developers

- **[Contributing Guide](CONTRIBUTING.md)** - How to contribute to FPM
- **[Changelog](CHANGELOG.md)** - Release history and changes

## 🛠️ CLI Commands

### Package Management

#### `fpm package`
Package a Frappe application into an `.fpm` file.

```bash
fpm package --version 1.0.0 --org myorg [--app-name my_app]
```

**Flags:**
- `--version` (required): Package version (e.g., 1.0.0)
- `--org`: Organization identifier (overrides auto-detection)
- `--app-name`: Frappe app name (overrides auto-detection)
- `--output-path`: Directory to save the `.fpm` file (default: current directory)
- `--overwrite`: Overwrite existing `.fpm` file
- `--skip-local-install`: Skip installing to local FPM store
- `--package-type`: Package type (prod|dev, default: prod)
- `--bundle-deps`: Bundle Python dependencies for offline install (default: true for prod, false for dev)
- `--platform`: Wheel platform tag of the **destination** bench, e.g. `manylinux2014_x86_64`;
  repeat to accept several (default: `manylinux2014_x86_64` for prod, packaging host
  otherwise; `--platform host` forces a host build)
- `--python-version`: Python version of the destination bench, e.g. `3.11`. **Required with
  `--platform` when the app has dependencies to vendor** — never guessed from the packaging host
- `--implementation`, `--abi`: further pip target constraints (default `cp`; ABIs derived by pip)
- `--bench-path`: a Frappe bench with node/yarn available; runs Frappe's own
  `bench build --app <app> --production` so the package ships compiled JS/CSS (see
  [Assets](#assets))
- `--build-frontend`: compile the app's JavaScript frontend — the Vite SPA apps like
  `frappe/crm` build into `<app>/public/frontend` (default: true, when the checkout
  declares one). `--build-frontend=false` packages without it (see
  [App frontends](#app-frontends))
- `--frontend-timeout`: time limit for the frontend install and for the frontend build,
  each (default `20m`)
- `--frontend-site-config`: a bench's `sites/common_site_config.json` to build the frontend
  against. Apps like `frappe/crm` compile `socketio_port` into their bundle; without this,
  Frappe's defaults are synthesized (see
  [Frontends that read the bench](#frontends-that-read-the-bench))
- `--no-bench-scaffold`: fail rather than build a bench-resolving frontend against a
  synthesized config
- `--repo`: repository to resolve `required_apps` against (default: local store, then every
  configured repository)

#### `fpm mirror`
Bulk-build the catalog's apps and publish the versions the registries do not have yet.

```bash
fpm mirror --repo ghcr --repo fpm-http --catalog catalog/apps.csv --python-version 3.11
```

`--repo` is **repeatable**, so one build of each version is published to several backends
at once — GHCR as OCI and an HTTP FPM registry together. A version is built when *any*
repository is missing it, so backends that started out of step converge on the same set;
publishing is idempotent per repository, and one that already has a version reports it
rather than failing the run.

`--cache-dir` (default `~/.fpm/build-cache`) is what makes a run cheap: git checkouts live
in `<cache>/src` and are fetched rather than re-cloned, and pip, npm and yarn are pointed at
`<cache>/{pip,npm}` for every build subprocess — within a run and, when the directory is
cached, across runs. The scheduled workflow in `.github/workflows/mirror.yml` does both.

**What happens, in order:**

1. The source tree is validated as a Frappe app (`<app>/hooks.py`, `__init__.py`,
   `modules.txt`) **before anything else** — no metadata, git or build work is done for a
   tree that is not a Frappe app, which fails with exit code 3.
2. The exact git commit is resolved and recorded as `commit_sha` (plus `git_ref`,
   `git_dirty`) in `app_metadata.json`.
3. `required_apps` from `hooks.py` are resolved to pinned `org/app==version` packages
   (local store first, then repositories) and recorded; an entry that cannot be resolved fails
   packaging with exit code 5. Required apps are **not** bundled — see
   [Required apps](#required-apps).
4. With `--bench-path`, assets are built; any build error fails packaging (exit code 4).
5. The app's JavaScript frontend is compiled when the checkout declares one, and its output
   packaged (see [App frontends](#app-frontends)); a build error fails packaging.
6. Python dependencies are vendored as wheels for the target, with a lock file.

**Exit codes:** `3` not a Frappe app · `4` asset build failed · `5` unresolved
`required_apps` · `1` anything else.

#### `fpm install`
Install a Frappe application package with automatic transitive dependency resolution.

```bash
fpm install <org>/<app>==<version> --bench-path /path/to/bench [--site mysite.local]
```

**Flags:**
- `--bench-path`: the Frappe bench to install into
- `--deps`: automatically resolve, fetch, and cascade-install missing transitive dependencies into the bench (default: true)
- `--no-deps`: install only the specified package without pulling dependencies
- `--dry-run`: compute and print the full installation plan and actions without mutating the bench
- `--rollback`: automatically rollback bench state on mid-flight failure using transactional LIFO journals (default: true)
- `--site`: also install the app onto a site, by running what `bench --site <site> install-app <app>` runs (`env/bin/python -m frappe.utils.bench_helper frappe …`)
- `--skip-required-apps-check`: do not fail when a required app is missing from the local store/bench
- `--ignore-platform-mismatch`: do not fail when the vendored wheels target another platform or Python version

**Pre-install checks & Transitive Resolution:**
- By default, FPM traverses `required_apps` (and any nested requirements), fetches missing packages from configured repositories (HTTP or OCI) into the local store, and installs them in deepest-first topological order.
- Before mutating the bench, an in-memory snapshot is recorded. On any installation error, the rollback engine restores the bench cleanly without deleting pre-existing apps.

#### `fpm publish`
Publish a package to an HTTP or OCI container repository.

```bash
fpm publish <org>/<app>==<version> [--repo repository-name]
fpm publish --from-file ./my-app-1.0.0.fpm --repo repository-name
```

### Repository Management

#### `fpm repo add`
Add a new FPM package repository (WebDAV/Nginx or OCI container registry).

```bash
# HTTP/WebDAV Repository
fpm repo add company-repo https://fpm.company.com --priority 10 --username deployer

# OCI Container Registry (e.g. GitHub Container Registry or local registry)
fpm repo add ghcr ghcr.io/vyogotech/fpm --type oci --username myuser
fpm repo add local-oci localhost:5000/fpm --type oci --plain-http
```

**Flags:**
- `--type`: Repository type (`http` [default] or `oci`)
- `--priority`: Priority order for package resolution (lower number = higher priority)
- `--username`: Username for authenticated operations
- `--plain-http`: Use plain HTTP for OCI registries (e.g. localhost development)
- `--insecure`: Skip TLS verification for self-signed OCI registries

### Authenticating to a Repository

Passwords and tokens are **never stored in plaintext `~/.fpm/config.json`**. Credentials are automatically resolved in priority order:

1. **Generic environment variables**: `REGISTRY_PASSWORD`, `FPM_REGISTRY_PASSWORD`, `FPM_REGISTRY_TOKEN`, `REGISTRY_USERNAME`, `FPM_REGISTRY_USERNAME`
2. **Repository-specific variables**: `FPM_REPO_<NAME>_PASSWORD` (`ghcr` → `FPM_REPO_GHCR_PASSWORD`)
3. **Docker/Podman credential store**: `~/.docker/config.json` (for OCI repositories)
4. **Interactive prompt**: masked prompt with no terminal echo

```bash
# Scripted / CI (GitHub Actions)
export REGISTRY_PASSWORD=${{ secrets.GITHUB_TOKEN }}
fpm publish myorg/my-app==1.0.0 --repo ghcr
```

#### `fpm repo list`
List all configured repositories and their types (`http` vs `oci`).

```bash
fpm repo list
```

#### `fpm repo remove`
Remove a configured repository.

```bash
fpm repo remove <name>   # alias: fpm repo rm
```

Packages already downloaded stay in the local FPM app store; only the configuration is
removed. If the repository was the default publish target, that is unset too.

#### `fpm repo default`
Set or show the default publish repository.

```bash
fpm repo default <repo-name>  # Set default
fpm repo default              # Show current default
```

### Package Discovery

#### `fpm search`
Search for packages across repositories.

```bash
fpm search                       # List local packages
fpm search erp                   # Search local store and cache by keyword
fpm search erp --remote          # Also search configured repositories
fpm search --remote              # List every package in every repository
fpm search myorg/my-app --remote # Look up an exact package
```

**Flags:**
- `--remote`: also query configured repositories. Without it, `fpm search` makes no
  network calls.

Remote keyword search uses each repository's package index (`/metadata/index.json`),
which `fpm publish` maintains automatically. A repository without an index can still be
queried for an exact `<org>/<app>`, but cannot be searched by keyword.

#### `fpm exists`
Answer "is this package already available?" from metadata alone — no artifact is
downloaded — so build tooling can skip a redundant build.

```bash
fpm exists myorg/my-app==1.2.0                                   # local store
fpm exists myorg/my-app --remote --commit 3f2a9c1                # any version built from this commit
fpm exists myorg/my-app==1.2.0 --remote --platform manylinux2014_x86_64 --python-version 3.11
fpm exists myorg/my-app --remote --json                          # machine-readable
```

**Flags:** `--commit <sha>` (prefix of ≥ 7 chars accepted), `--platform <tag>`,
`--python-version <ver>`, `--remote` (query configured repositories' `package-metadata.json`),
`--repo <name>`, `--json`. Exit status `0` when a matching package exists, `10` when none
does, `1` on error. Without `==<version>`, the newest matching version wins; candidates that
exist but fail a constraint are listed with the reason.

#### `fpm bundle`
Export a package with every Frappe app it transitively requires (each once) into a
directory with an install-order manifest, for a single `fpm install <dir>` on an offline
bench. See [Required apps](#required-apps).

```bash
fpm bundle frappe/hrms==16.0.0 --output ./hrms-bundle           # from the local store
fpm bundle ./hrms-16.0.0.fpm --remote                            # fetch missing required apps first
```

**Flags:** `--output/-o <dir>` (default `<app>-<version>-bundle`), `--remote`, `--repo <name>`.

#### `fpm get-app`
Download a package from a repository to local store.

```bash
fpm get-app <repo-name>/<org>/<app>[:<version>]
```

**Example:**
```bash
fpm get-app company-repo/frappe/erpnext:15.0.0
fpm get-app company-repo/frappe/erpnext  # Gets latest
```

### Utilities

#### `fpm deps`
Inspect the dependencies a package declares and bundles, and calculate the exact installation plan and actions for a target bench.

```bash
fpm deps ./my-app-1.0.0.fpm                      # a package file
fpm deps myorg/my-app                            # latest in local store / configured repos
fpm deps frappe/hrms==15.2.0 --bench-path /bench # check against bench apps
fpm deps frappe/hrms --json                      # machine-readable installation queue & plan
```

Reports:
- Declared and vendored Python dependencies / wheels.
- Required Frappe apps (`hooks.py` `required_apps`) with transitive closure.
- Installation Plan & Order: Categorizes each app as `SKIP` (already satisfied in `--bench-path`), `INSTALL` (in local store), or `FETCH & INSTALL` (from configured HTTP/OCI repository).

**Flags:** `--bench-path <path>`, `--repo <name>`, `--no-remote`, `--check` (exit non-zero if requirements cannot be satisfied), `--json` (emits `install_plan` and `install_queue` arrays).

#### `fpm mirror`
Bulk-build and publish official Frappe apps from the checked-in catalog.

```bash
fpm mirror --catalog catalog/apps.csv --repo company-repo            # full catalog
fpm mirror --catalog catalog/apps.csv --repo company-repo --dry-run  # plan only
fpm mirror --catalog catalog/apps.csv --repo company-repo --apps crm,hrms
```

Discovers the latest release tag of each major line per app (`git ls-remote`,
no clone), skips versions the repository already has, and builds and publishes
the rest. Re-runs are idempotent. Git checkouts, pip's wheel cache, and
npm/yarn caches persist under `~/.fpm/build-cache/` (override with
`--cache-dir`). Publishing credentials come from `FPM_REPO_<NAME>_PASSWORD`,
like `fpm publish`. See [catalog/README.md](catalog/README.md) for the catalog
format, branch-tracked apps, and per-app asset-build scripts.

**Flags:** `--apps` (slug filter), `--dry-run` (+ `--json`), `--skip-publish`,
`--output-path` (default `dist`), `--report <file.json>`, `--cache-dir`,
`--no-clean`. Exit codes: 0 clean, 1 when any app failed, 2 on
configuration/catalog errors.

## 🏢 Deploy Your Own Repository

FPM includes everything you need to deploy an enterprise-grade package repository using Nginx and Podman/Docker Compose.

### Quick Deploy

```bash
# Download repository deployment package
tar -xzf fpm-repository-v1.0.0.tar.gz
cd fpm-repository-v1.0.0

# Setup and start
./scripts/setup.sh
podman compose up -d

# Verify
curl http://localhost:8080/health
```

### Features

- ✅ Nginx with WebDAV for package uploads
- ✅ HTTP Basic Authentication
- ✅ Health checks and monitoring
- ✅ Large file support (500MB+)
- ✅ Automated setup scripts
- ✅ Production-ready configuration

See **[Repository Setup Guide](fpm-repo-README.md)** for detailed instructions.

## 🏗️ Architecture

```
┌─────────────────────────────────────┐
│         FPM CLI Client              │
│  (Go - Cross Platform)              │
│  - Package creation                 │
│  - Repository management            │
│  - Package installation             │
└──────────────┬──────────────────────┘
               │ HTTP/HTTPS
               ▼
┌─────────────────────────────────────┐
│      FPM Repository Server          │
│  (Nginx + WebDAV)                   │
│  - Package storage                  │
│  - Metadata management              │
│  - Authentication                   │
└──────────────┬──────────────────────┘
               │
               ▼
┌─────────────────────────────────────┐
│     Package Structure               │
│  /<org>/<app>/<version>/*.fpm       │
│  /metadata/<org>/<app>/*.json       │
│  /metadata/index.json  (catalogue)  │
└─────────────────────────────────────┘
```

## 📦 Package Format

FPM packages are ZIP archives with a standardized structure:

```
my-app-1.0.0.fpm
├── app_metadata.json         # Metadata: name, version, commit_sha, required_apps, wheel target,
│                             #   asset_bundles, frontend_dirs, frontend_routes
├── my_app/                   # Main application module
│   ├── __init__.py
│   ├── hooks.py
│   ├── public/
│   │   ├── dist/             # Compiled JS/CSS from `fpm package --bench-path` (js/, css/, css-rtl/)
│   │   └── frontend/         # Compiled Vite SPA (crm, helpdesk, …); served at /assets/<app>/frontend/
│   ├── www/
│   │   └── my_app.html       # The SPA's route, rendered by Frappe at /my_app
│   └── ...
├── requirements.txt          # Python dependencies (either or both)
├── pyproject.toml            # PEP 621 project metadata
├── package.json              # Node dependencies (optional)
├── wheels/                   # Bundled Python deps (prod default)
│   └── fpm-lock.txt          # Every vendored distribution as name==version
├── compiled_assets/          # Legacy prebuilt assets (older packages; merged into public/ on install)
└── assets/                   # Additional assets
```

### Assets

`fpm package --bench-path <bench>` runs Frappe's own asset build for the app — what
`bench build --app <app> --production` runs — inside the given bench, and ships the output
where Frappe leaves it: `<app>/public/dist/{js,css,css-rtl}/<name>.bundle.<HASH>.*`. The
resulting manifest entries are recorded as `asset_bundles` in `app_metadata.json`. Any build
error fails packaging; there is no silent source-only fallback.

Frappe apps are built from inside `<bench>/apps/`: their frontends locate the bench from
their own path (erpnext's `banking/`, hrms's `frontend/` read `../../../sites/…`). So a
source that is not already `<bench>/apps/<app>` is **staged there as a copy** (without
`.git`/`node_modules`) for the build, the package is created from that copy, and the copy
is removed afterwards; a source that already lives in `apps/` is built in place. When the
app has a `package.json`, `yarn install --check-files` runs first, exactly as `bench get-app`
does, so imports like erpnext's `onscan.js` resolve and the app's own `yarn build` (run by
Frappe's build) works. `node_modules/` never enters the package.

Building at `apps/<app>` also matters for the bundle names: esbuild folds the input
source paths into each bundle's `[hash]`, so `desk.bundle.<HASH>.js` is only reproducible
when the app is built from the same relative location a bench keeps it in. Packages built
this way carry exactly the names a `bench build` on the target would produce.

`fpm install` then does exactly what `bench build --app <app> --using-cached` does with a
prebuilt `public/dist`, ported from `frappe/build.py` and `esbuild/esbuild.js`:

- links `sites/assets/<app>` → the app's `public/` directory (plus `node_modules` and
  `_docs` links when present), as `frappe.build.make_asset_dirs` does;
- **merges** the app's bundles into the single global `sites/assets/assets.json` (and
  `rtl_`-prefixed keys into `assets-rtl.json`): existing keys of other apps are preserved
  in place, new keys appended, written as `JSON.stringify(obj, null, 4)` with no trailing
  newline — the same file Frappe would produce; there is no per-app manifest fragment;
- deletes the `assets_json` key from the bench's `redis_cache`, as the build does, so a
  running site picks up the new bundle paths.

No Node toolchain is needed on the target machine.

### App frontends

Apps like `frappe/crm`, `frappe/helpdesk`, `frappe/insights` and `frappe/gameplan` ship a
second, different asset scheme: a **Vite single-page app** in its own directory with its own
`package.json`. `bench build` never produces it — Frappe's esbuild only globs `*.bundle.*`
and an SPA has none — and the output is listed in the app's own `.gitignore`, so it is
absent from every fresh checkout:

```
crm/                          the checkout
├── package.json              "build": "cd frontend && yarn build"
├── frontend/                 the Vite project (src/, vite.config.js, yarn.lock)
└── crm/                      the Python module
    ├── public/frontend/      ← BUILD OUTPUT, gitignored: served at /assets/crm/frontend/
    └── www/crm.html          ← BUILD OUTPUT, gitignored: Frappe renders it at /crm
```

`fpm package` compiles it by default and ships the result, so the package installs into a
bench that has no Node toolchain at all. Concretely it:

- **finds the project**, preferring the checkout's root `package.json` when it has a `build`
  script — that is the app's own entry point and it already delegates (`cd frontend && yarn
  build`), so the root and the subdirectory are never built twice. Otherwise `frontend/`,
  `desk/` (helpdesk), `dashboard/`, `ui/` and `<app>/frontend/` are tried in that order;
- **installs and builds** with the package manager the lockfile names (yarn, pnpm or npm),
  always forcing `devDependencies` on — crm keeps `autoprefixer`, `postcss` and `tailwindcss`
  there, so an install that honours an inherited `NODE_ENV=production` succeeds and the build
  then dies on a missing module;
- **discovers the output** rather than assuming a name, because there is no convention to
  assume. A directory under `<app>/public` holding an `index.html` is an SPA root; one named
  `dist` holding files is bundler output; and whatever the build itself just wrote to counts
  regardless of what it is called;
- **reads the route from the app's `hooks.py`**, never from the app or directory name. The
  `to_route` values of `website_route_rules` are the only reliable source — see the table
  below for how little the names have in common. A template is treated as *this frontend's*
  route only when it actually loads the built output (it links `/assets/<app>/<dir>/…`, or
  is the byte-identical copy `copy-html-entry` makes), so erpnext's two dozen DocType portal
  routes are not mistaken for frontend routes;
- **writes the route template** when the app's build script does not — crm's and erpnext's
  end in `copy-html-entry`, others stop at `vite build` and leave the SPA with no route at
  all. Only when the app declares exactly one route and builds exactly one SPA, so the
  mapping is certain; otherwise fpm reports it rather than inventing a filename. An existing
  template is never overwritten;
- **fails packaging** when a declared frontend builds but writes nothing servable. A package
  that installs cleanly and then serves a blank page is worse than a failed build.

Nothing about the layout is conventional, which is why every part of it is read rather than
guessed:

| app | build script | output directory | route (`hooks.py` `to_route`) |
|---|---|---|---|
| `frappe/crm` | `cd frontend && yarn build` | `crm/public/frontend` | `crm` |
| `frappe/drive` | `cd frontend && yarn build` | `drive/public/frontend` | `drive` |
| `frappe/insights` | `cd frontend && yarn build` | `insights/public/frontend` | `_insights` |
| `frappe/builder` | `cd frontend && yarn build` | `builder/public/frontend` | `_builder` |
| `frappe/gameplan` | `cd frontend && yarn build` | `gameplan/public/frontend` | `g` |
| `frappe/helpdesk` | `cd desk && yarn build` | `helpdesk/public/desk` | `helpdesk` (as `www/helpdesk/index.html`) |
| `frappe/erpnext` | `cd banking && yarn build` | `erpnext/public/banking` | `banking`, plus 23 unrelated DocType routes |
| `frappe/hrms` | `yarn build-pwa && yarn build-roster` | two outputs | `hrms`, `roster` |

The results are recorded as `frontend_built`, `frontend_dirs` and `frontend_routes` in
`app_metadata.json`. `fpm install` serves them through the same `sites/assets/<app>` →
`<app>/public` symlink, so `crm/public/frontend/…` is reachable at `/assets/crm/frontend/…`
with no manifest entry — `assets.json` only ever tracks `*.bundle.*` files.

#### Frontends that read the bench

Some of these frontends resolve the bench from their own physical path. crm, gameplan,
helpdesk and drive each carry one such import, in their socket module:

```js
import { socketio_port } from '../../../../sites/common_site_config.json'
```

Four levels up from `<bench>/apps/<app>/frontend/src` is the bench root, so the file only
exists when the checkout sits at `<bench>/apps/<app>`. (insights and builder have no such
import and build anywhere.)

fpm handles this without a bench: it scans the frontend's sources for the import and, when
the file is missing, **writes Frappe's default `sites/common_site_config.json` next to the
checkout for the build and removes it afterwards.** An existing config — a real bench — is
never touched, and if that location cannot be written the checkout is staged into a
throwaway bench instead. So `fpm package ./crm` works from any directory.

The defaults are the right contents for a normal deployment. `socketio_port` is the only key
any app's frontend reads, and it is compiled in only to be used when `window.location.port`
is set — a bench served directly on a port. Behind nginx or a Kubernetes ingress on 80/443
the browser sees no port and the value is never read, so a real bench's config would produce
an identical bundle. Where it does matter, pass
`--frontend-site-config <bench>/sites/common_site_config.json`; `--no-bench-scaffold` refuses
to build against synthesized values at all.

Use `--build-frontend=false` to package without the frontend — for a checkout you have
already built yourself, or a deliberately source-only package.

### Required apps

`hooks.py` `required_apps` (e.g. `["frappe", "frappe/erpnext"]`) are **never bundled**: a
shared dependency such as erpnext would otherwise be duplicated inside every custom app.
Instead:

- `fpm package` resolves each entry (frappe excepted) to an exact published package —
  local store first, then configured repositories — and records it in `app_metadata.json`
  as `required_apps: [{org, name, version, requirement}]`. Unresolvable entries fail
  packaging (exit 5). Entry names are parsed like Frappe's own installer does
  (`erpnext`, `frappe/erpnext`, a git URL, `…@branch` all name `erpnext`).
- `fpm install` walks the transitive closure and requires every pin to be present in the
  local FPM store; missing ones are a hard error (exit 6), never a fetch. Install packages
  in dependency order.
- **Apps already in the bench count.** A required app that the bench itself provides —
  installed with `bench get-app`, or baked into an image such as the ERPNext single-node
  one — satisfies the requirement when its module's `__version__` matches the pin (or the
  pin is unversioned); it is never reinstalled, and its own requirements are the bench's
  concern. At package time, `--bench-path` resolves entries against the build bench's apps
  the same way (after the local store, before repositories), so packaging hrms inside an
  ERPNext image pins `frappe/erpnext==<that erpnext's version>` without erpnext ever being
  packaged. `fpm deps --bench-path <bench>` shows which requirements a bench provides.
- `fpm deps` shows the closure and what is missing; `fpm exists` and published
  `package-metadata.json` carry the same pins so tooling can check before starting.
- **To ship an app with everything it needs in one unit**, export a *closure bundle*: a
  directory holding the app's package and the package of every app it transitively
  requires — each exactly once — plus `fpm-bundle.json` listing them in install order.

  ```bash
  fpm package hrms --with-deps -v 16.0.0 --python-version 3.11 --bench-path ~/bench
  #  -> hrms-16.0.0.fpm and hrms-16.0.0-bundle/{erpnext-16.0.0.fpm, hrms-16.0.0.fpm, fpm-bundle.json}
  fpm bundle frappe/hrms==16.0.0 --output ./hrms-bundle [--remote]   # same, from the local store
  # on the offline bench:
  fpm install ./hrms-16.0.0-bundle --bench-path ~/frappe-bench --site mysite.local
  ```

  `fpm install <bundle-dir>` installs the packages in manifest order, so each step's
  required-apps check passes. Dependencies are still not duplicated inside packages:
  erpnext appears once in the bundle, whichever apps require it. A requirement the build
  bench already provides (`--bench-path`, e.g. erpnext inside an ERPNext image) is listed in
  the manifest as `"provided_by": "bench"` and not shipped; the target bench must have it
  too, at that version, or the install fails.

### Offline Installation

**Production packages bundle their Python dependencies by default**, so they install
without network access. Development packages do not, since bundling only slows the local
iteration loop.

```bash
# production package: bundles deps, cross-built for amd64 Linux
fpm package -v 1.0.0

# development package: no bundled deps, installs from the network
fpm package -v 1.0.0 --package-type dev

# opt out for a production package
fpm package -v 1.0.0 --bundle-deps=false

# opt in for a development package (built for the packaging host)
fpm package -v 1.0.0 --package-type dev --bundle-deps

# explicit target: the destination bench's platform(s) and interpreter
fpm package -v 1.0.0 --platform manylinux2014_x86_64 --platform manylinux_2_28_x86_64 --python-version 3.11

# ...and build the JS/CSS while at it
fpm package -v 1.0.0 --python-version 3.11 --bench-path ~/frappe-bench
```

The destination's interpreter version is **explicit input**: pip resolves wheels for
`--python-version`, never for the packaging host's interpreter, and a cross-build with
dependencies but no `--python-version` fails packaging. Every vendored distribution is
pinned in `wheels/fpm-lock.txt`.

Dependencies are read from **both `requirements.txt` and `pyproject.toml`**, so apps on
either convention work without configuration. An app carrying both has its specifiers
merged and de-duplicated. From `pyproject.toml` FPM reads:

- `[project].dependencies` — the app's runtime dependencies
- `[build-system].requires` — the build backend, which pip needs to perform the editable
  build on the target machine; without it bundled, an offline install fails before it starts

Optional extras (`[project.optional-dependencies]`) are not bundled, since pip does not
install them by default.

Bundling needs `python3` with `pip` on PATH at packaging time. If a dependency publishes
no wheel for the target platform, packaging fails rather than producing a package that
cannot install offline; `--bundle-deps=false` packages without them.

Installing a package that bundles `wheels/` pins pip to that directory
(`--no-index --find-links wheels/`), so **no network access is required**. Packages
without vendored wheels keep resolving from the network, unchanged.

The platform and Python version the wheels were built for are recorded as `wheel_platform`
and `wheel_python_version` in `app_metadata.json`, and `fpm install` **refuses** to start
when they do not match the installing host and the bench's `env/bin/python` (exit 7) —
there is no network fallback once pip runs offline. `--ignore-platform-mismatch` lets pip
decide instead. Vendored wheels are covered by the package's `content_checksum` like any
other file.

> **Node dependencies are not vendored, and do not need to be.** `node_modules` is a
> build-time requirement for producing JS/CSS; `fpm package --bench-path` builds the
> output and ships it in `<app>/public/dist/` (see [Assets](#assets)). No Node toolchain is
> needed on the target machine.

An end-to-end test of all of this against a real, network-isolated bench lives in
[test/offline](test/offline/README.md).

## 🔒 Security

- **Authentication**: HTTP Basic Auth for repository write operations
- **Integrity**: SHA256 checksums verified end to end (see below)
- **Encryption**: TLS/SSL support (configurable)
- **Access Control**: Granular permissions per repository
- **Audit**: Access logs for all operations

### Package Integrity

Every package carries two distinct SHA256 checksums, each answering a different question:

| Checksum | Location | Covers | Verified by |
|----------|----------|--------|-------------|
| `content_checksum` | `app_metadata.json`, inside the `.fpm` | The extracted payload — app module, `requirements.txt`, `package.json`, `install_hooks.py`, `compiled_assets/` | `fpm publish`, before upload |
| `checksum_sha256` | `package-metadata.json`, in the repository | The raw `.fpm` archive bytes | `fpm install` / `fpm get-app`, on every download **and** every cache hit |

Together these close the chain from packaging to installation:

1. `fpm package` records `content_checksum` over the fully staged payload.
2. `fpm publish` re-derives it from the archive and **refuses to upload** on mismatch, so a package modified after it was built never reaches a repository.
3. The repository records `checksum_sha256` over the uploaded bytes.
4. `fpm install` rejects any download whose bytes do not match, deletes the offending file, and falls through to the next configured repository. Cached packages are re-verified before reuse, so a poisoned cache entry is discarded and re-downloaded rather than installed.

A package whose repository metadata records no `checksum_sha256` **cannot be verified, and
is rejected**. Accepting it would let any repository skip verification simply by omitting
the field — a weaker guarantee than no verification at all, since the client would report
success while checking nothing. Republish such a package to record a checksum.

## 🧪 Development

### Prerequisites

- Go 1.22.2 or higher
- Git
- (Optional) Docker/Podman for repository testing

### Build from Source

```bash
# Clone the repository
git clone https://github.com/vyogotech/fpm.git
cd fpm

# Download dependencies
go mod download

# Build
go build -o fpm ./cmd/fpm/main.go

# Run tests
go test -v ./...

# Run
./fpm --help
```

### Development with Dev Containers

This project supports VS Code Dev Containers for consistent development environments:

1. Install Docker Desktop and VS Code Remote Containers extension
2. Open project in VS Code
3. Click "Reopen in Container" when prompted

See [Contributing Guide](CONTRIBUTING.md) for more details.

## 🤝 Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) for:

- Code of conduct
- Development workflow
- Coding standards
- Testing requirements
- Pull request process

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Inspired by npm, Maven, and other package managers
- Built for the Frappe/ERPNext community
- Uses [Cobra](https://github.com/spf13/cobra) for CLI framework

## 📞 Support

- **Issues**: [GitHub Issues](https://github.com/vyogotech/fpm/issues)
- **Discussions**: [GitHub Discussions](https://github.com/vyogotech/fpm/discussions)
- **Documentation**: [Project Wiki](https://github.com/vyogotech/fpm/wiki)

## 🗺️ Roadmap

See [CHANGELOG.md](CHANGELOG.md) for planned features including:

- Authentication support in FPM client
- Automatic TLS/SSL configuration
- Web UI for repository browsing
- Package signing and verification
- Repository mirroring
- Docker registry-style API

---

**Made with ❤️ for the Frappe community**
