# Changelog

All notable changes to the Frappe Package Manager (FPM) project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [3.0.0] - 2026-08-27

Pluggable OCI Registry backend, generic non-provider-specific authentication, OCI 1.1 referrers graph linking, Maven-style transitive dependency resolution, topological cascade installation, pre-install snapshot recording with transactional LIFO rollback, enhanced `fpm deps` inspection utility, GHCR catalog prepopulation workflow with multi-tier caching, and real SNE container integration tests.

This release also makes fpm build and package the JavaScript frontends that apps such
as frappe/crm, frappe/helpdesk, frappe/insights and frappe/gameplan ship, and mirror one
build into several registry backends at once.

### Added

- Frontends that resolve the bench from their own path — crm, gameplan, helpdesk and drive
  each import `../../../../sites/common_site_config.json` — no longer need a bench to build
  against. When the file is missing, fpm writes Frappe's defaults next to the checkout for
  the build and removes it afterwards, so `fpm package ./crm` works from any directory. An
  existing config is never touched; if the location cannot be written, the checkout is
  staged into a throwaway bench instead. `socketio_port` is the only key any app reads and
  it is inert behind nginx or a Kubernetes ingress, so the defaults produce the same bundle
  a real bench would; `--frontend-site-config` supplies real values where it matters and
  `--no-bench-scaffold` refuses synthesized ones.
- The mirror fetches **build-time dependencies**: another bench app whose source a build
  reads off disk. fpm resolved `required_apps` to pinned versions when packaging and
  cascade-installed them when installing, but neither helps a build that runs
  `cd ../../frappe/ui && yarn install`, as helpdesk's does — it failed with "can't cd to
  ../../frappe/ui". Such references are found by scanning the project's package.json
  scripts, and the apps are checked out beside it at their newest release. They are read,
  never built or published.
- The catalog gains a `tier` column, and the mirror workflow runs one wave per tier.
  Sharding the catalog one app per runner removed the ordering that used to make an
  app's `required_apps` available before it was built: `hrms` could not pin `erpnext`
  or hang its OCI referrers subject off it, and `webshop` failed packaging outright.
  `fpm mirror --list-tiers` and `--list-slugs --tier N` drive the waves.
- `fpm mirror` now honours `--allow-third-party`, which was accepted and then never read
  — every third-party catalog entry was built regardless. It defaults to false: the
  mirror publishes the frappe organisation's own apps, and an entry from another
  organisation is disabled rather than dropped, so naming it says why. `frappe` itself is
  no longer mirrored: every bench image ships the framework, and its assets come from
  `bench build`, not an app frontend.
- `fpm mirror --repo` is repeatable, so one run builds each version once and publishes it
  to several backends — GHCR as OCI and an HTTP FPM registry together. A version is built
  when *any* repository is missing it, so backends that started out of step converge;
  publishing is idempotent per repository, and one that already has a version reports it
  rather than failing the run. The mirror workflow does this, adding the HTTP registry when
  `vars.FPM_REGISTRY_URL` is set and mirroring to GHCR alone when it is not.
- `fpm package --build-frontend` (default true): compile the app's JavaScript frontend when
  the checkout declares one. `--build-frontend=false` packages without it.
- `fpm package --frontend-timeout` (default `20m`): bounds the frontend install and build.
- `app_metadata.json` records `frontend_built`, `frontend_dirs`, `frontend_routes` and
  `frontend_source`, so tooling can see what a package carries without unpacking it.
- A frontend that builds but writes nothing servable now fails packaging, rather than
  shipping an archive that installs and then serves a blank page. When the failure is a
  frontend resolving the bench from its own path (crm's `frontend/src/socket.js` imports
  `../../../../sites/common_site_config.json`), the error says to use `--bench-path`.

- **Pluggable OCI Container Registry Backend (`type: oci`)**:
  - `internal/ociregistry`: Implemented full OCI registry integration via `oras.land/oras-go/v2`.
  - Added `--type`, `--plain-http`, and `--insecure` flags to `fpm repo add` (e.g. `fpm repo add ghcr ghcr.io/vyogotech/fpm --type oci`).
  - Single-layer `.fpm` payload mapping (`application/vnd.vyogo.fpm.package.v1.fpm`) with content-addressed SHA256 layer digest verification for zero-overhead integrity verification.
  - Manifest annotation promotion: `app_metadata.json` attributes (commit SHA, git ref, wheel platform, python version, Frappe compatibility, required apps) are promoted directly to OCI image manifest annotations, queryable without downloading the payload.
  - OCI 1.1 `subject` descriptor and referrers API linking for `required_apps` (e.g. `hrms` referencing `erpnext`), discoverable via `oras discover`.
  - Seamless integration across `fpm publish`, `fpm install`, `fpm exists`, `fpm mirror`, and `fpm deps`.
- **Generic Registry Authentication**:
  - Prioritized credential resolution using non-provider-specific environment variables: `REGISTRY_PASSWORD`, `FPM_REGISTRY_PASSWORD`, `FPM_REGISTRY_TOKEN`, `REGISTRY_USERNAME`, `FPM_REGISTRY_USERNAME`.
  - Standard Docker / Podman credential store fallback (`~/.docker/config.json`).
  - Interactive terminal password prompt via `term.ReadPassword` fallback.
- **Transitive Dependency Cascade & Resolution (`fpm install`)**:
  - By default (`--deps=true`), `fpm install` queries all configured repositories (HTTP and OCI) to automatically fetch and unpack missing transitive dependencies into `~/.fpm/apps/` before bench installation.
  - Computes topological dependency order and installs base dependencies first.
  - Added `--no-deps` flag to install only the target package without pulling dependencies.
  - Added `--dry-run` flag to print the full dependency resolution and installation execution plan without mutating the bench.
- **Dependency Inspection & Installation Plan Utility (`fpm deps`)**:
  - `cmd/deps.go`: Enhanced `fpm deps <package>` to resolve local files, local store packages, or remote repositories.
  - Traverses the recursive `required_apps` closure and categorizes each app into `SKIP` (already present in bench), `INSTALL` (cached in store), or `FETCH & INSTALL` (from remote repository).
  - Added `--bench-path` to evaluate pre-existing bench installations.
  - Added `--json` flag returning `install_plan` and `install_queue` arrays for automation tooling.
- **Pre-Install Snapshot & Atomic Rollback Engine**:
  - `internal/snapshot`: Takes an in-memory snapshot of the target bench before applying any mutations, recording pre-existing apps, versions, raw `apps.txt` bytes, and asset manifests.
  - `internal/rollback`: Records LIFO transactional rollback actions (`SymlinkAction`, `AssetDeployAction`, `PipInstallAction`, `AppsTxtAction`). On mid-flight installation failures (when `--rollback=true`, default), the rollback engine cleanly undoes intermediate steps while guaranteeing that pre-existing apps are never deleted or uninstalled.
  - `internal/assets`: Implemented `Manifest.Delete` and `assets.Undeploy` to remove asset symlinks, scrub app entries from `assets.json` and `assets-rtl.json`, and invalidate Redis cache.
- **Exit Codes**:
  - `ExitRolledBack` (8): Returned when an install fails and the bench is restored cleanly to its pre-install state.
  - `ExitVersionConflict` (9): Returned when a required dependency conflicts with a different version already present in the bench.
- **GitHub Container Registry (GHCR) Catalog Prepopulation (`.github/workflows/mirror.yml`)**:
  - Automated weekly cron and manual `workflow_dispatch` CI workflow to bulk-build, vendor, package, and publish official Frappe apps (`frappe`, `erpnext`, `hrms`, `payments`, `wiki`, `crm`, `helpdesk`, `lms`, `raven`, etc.) to `ghcr.io`.
  - Multi-tier intra-run and inter-run persistent caching (`actions/cache@v4`) for wheel dependencies, git checkouts, npm/yarn asset compiler caches, and `.fpm` artifacts.
- **SNE Offline Integration Test Suite**:
  - Added real Single Node Environment (SNE) container test harness (`test/offline/run.sh`) covering network isolation (`--network none`), live HTTP endpoint asset resolution, and real HRMS + ERPNext packaging and installation.
  - Added automated `offline-integration` test job in `.github/workflows/ci.yml`.

### Fixed

- **Apps with a JavaScript frontend (`frappe/crm`, `frappe/helpdesk`, `frappe/insights`,
  `frappe/gameplan`, erpnext's `banking`) packaged without their compiled frontend.** These
  apps ship a Vite SPA that Frappe's own esbuild never builds — it only globs `*.bundle.*`,
  and an SPA has none — and whose output is listed in the app's `.gitignore`
  (crm ignores `crm/public/frontend` and `crm/www/crm.html`). So neither `fpm package` nor
  `fpm package --bench-path` produced it, and the resulting package installed cleanly and
  then served a blank page. `fpm package` now compiles the app's frontend and ships the
  result. See [App frontends](README.md#app-frontends).
- Frontend dependency installs no longer honour an inherited `NODE_ENV=production`, which
  makes yarn skip `devDependencies`. crm keeps `autoprefixer`, `postcss` and `tailwindcss`
  there, so the install succeeded and the build then died with
  `Cannot find module 'autoprefixer'`. `fpm package` and `--bench-path`'s `yarn install`
  both force them on now.
- `fpm install` no longer reports "No built bundles ... package with `--bench-path`" for an
  SPA-only app, which sent users after a rebuild that cannot produce anything for it. An SPA
  is served through the `sites/assets/<app>` symlink and needs no `assets.json` entry.
- `fpm install` writes the SPA's route template when a package carries the compiled
  frontend without one; the app otherwise has no route to render it at. The name is read
  from the app's `hooks.py` `website_route_rules`, because it follows no convention — crm
  routes at `crm` but insights routes at `_insights`, builder at `_builder` and gameplan at
  `g`. When the app declares no route, or declares several so the mapping is ambiguous (as
  erpnext does, with 24 `to_route` values that mostly name DocTypes), fpm reports it instead
  of inventing a filename.
- The GHCR mirror built every directory with a `build` script, so crm was built twice (its
  root script is `cd frontend && yarn build`), and copied Vite output into
  `<app>/public/dist` — the directory reserved for the hashed `*.bundle.*` files that go
  into `assets.json`. It now shares one implementation with `fpm package`.
- Fixed CI `gofmt` whitespace and comment formatting failure on `cmd/install_test.go` and `internal/assets/assets.go`.

## [2.2.0] - 2026-08-27

Offline installation of custom (non-catalog) apps from arbitrary git checkouts: fpm
now does all the build, vendor and install work itself, and exposes enough metadata
for external caching and orchestration to key on.

### Added

- **`fpm package --bench-path <bench>`** runs Frappe's own asset build
  (`bench build --app <app> --production`) inside a bench and ships the output in
  `<app>/public/dist/`, recording the bundle manifest entries as `asset_bundles` in
  `app_metadata.json`. Any build error fails packaging (exit 4); there is no silent
  source-only fallback. `compiled_assets/` is still accepted from older packages.
  A source outside `<bench>/apps/` is staged there as a copy for the build (app
  frontends such as erpnext's `banking/` resolve the bench from their own path, and
  esbuild folds input source paths into bundle hashes, so building at `apps/<app>`
  yields the same hashed names a bench build would), and
  `yarn install --check-files` runs first when the app has a `package.json`, as
  `bench get-app` does. `node_modules/` is excluded from packages, and gitignore-style
  directory patterns (`node_modules/`, `tests/`, `__pycache__/`) now skip the directory
  itself instead of leaving empty entries.
- **`fpm install` deploys assets the way `bench build` does**, ported from
  `frappe/build.py` and `esbuild/esbuild.js` rather than invented: `sites/assets/<app>`
  is a symlink to the app's `public/` (`make_asset_dirs`), the app's bundles are
  merged into the single global `sites/assets/assets.json` / `assets-rtl.json`
  (`Object.assign` semantics — other apps' keys preserved, `JSON.stringify(obj, null, 4)`,
  no trailing newline), and the `assets_json` key is deleted from `redis_cache`. The
  previous `copyDirContents` deploy wrote a real directory and never touched the manifest,
  so Frappe could not resolve the bundles.
- **`commit_sha`, `git_ref`, `git_dirty`** in `app_metadata.json` and `commit_sha` /
  `git_ref` in published `package-metadata.json`: the exact revision a package was built
  from, for build de-duplication.
- **`fpm exists <org>/<app>[==<version>] [--commit] [--platform] [--python-version] [--remote] [--json]`**
  answers whether a package already exists — locally or in a repository — from metadata
  alone, without downloading; exit 10 when it does not.
- **`required_apps` (hooks.py) are resolved and enforced.** `fpm package` pins each entry
  to `org/app==version` against the local store and configured repositories and records
  them (`required_apps` in `app_metadata.json` and repository metadata); an unresolvable
  entry fails packaging (exit 5). `fpm install` checks the transitive closure is already in
  the local FPM store before touching the bench and fails hard (exit 6) instead of
  fetching. `fpm deps` shows the closure with presence, `--check` and `--json`.
  `internal/resolver` (previously an empty stub) implements this.
- **Explicit wheel target.** `--platform` is repeatable and `--python-version`
  (plus `--implementation`, `--abi`) is required for a cross-build with dependencies, so
  wheels are resolved for the destination bench's interpreter, never the packaging
  host's. Every vendored distribution is pinned in `wheels/fpm-lock.txt`;
  `wheel_python_version` is recorded in metadata.
- **`fpm install` refuses a platform/interpreter mismatch** (exit 7) instead of
  warning, since there is no network fallback once pip runs offline;
  `--ignore-platform-mismatch` restores the old behaviour. `--skip-required-apps-check`
  opts out of the required-apps check.
- **Bench-provided required apps.** A `required_apps` entry satisfied by an app
  already in the bench (installed outside fpm, or baked into an image such as the
  ERPNext single-node one) is accepted at install time when its module `__version__`
  matches the pin, and is never reinstalled; `fpm package --bench-path` resolves
  entries against the build bench the same way (`resolved_from: bench:<path>`), and
  `fpm deps --bench-path` reports them.
- **Closure bundles.** `fpm bundle <org>/<app>[==<version>] [--remote]` and
  `fpm package --with-deps` export a directory with the package plus the packages
  of every app it transitively requires — each once — and an `fpm-bundle.json`
  install-order manifest; `fpm install <bundle-dir>` installs them in that order,
  offline. This is how an app such as hrms (`required_apps = ["frappe/erpnext"]`)
  ships with erpnext without erpnext being duplicated inside its package.
- Distinct exit codes: 3 not a Frappe app, 4 asset build failed, 5 unresolved
  required apps, 6 missing required apps, 7 platform mismatch, 10 not found.
- `test/offline`: an end-to-end offline install scenario against a real,
  network-isolated Single Node Frappista container on a remote podman host
  (`make test-offline`).

### Changed

- **`fpm package` validates the Frappe app structure first**, before metadata
  generation, git introspection, dependency resolution or any build, and reports a
  typed error (`apputils.ErrNotFrappeApp`, exit 3) so callers can tell "not a Frappe
  app" apart from a build or network failure. The app module is detected from the
  directory layout (`--app-name`, else the single directory holding `hooks.py`,
  `__init__.py`, `modules.txt`).
- `fpm install --site` no longer needs a `bench` executable inside the virtualenv: it
  runs `env/bin/python -m frappe.utils.bench_helper frappe --site <site> install-app
  <app>` from `sites/`, which is what the `bench` CLI itself execs.
- `fpm package` installs into the local store through `appstore.ManageAppInLocalStore`
  instead of a duplicated inline extractor.

### Fixed

- `FPM_APPS_BASE_PATH` was ignored on the very first run (`InitConfig` returned the
  defaults without applying the override).

## [2.1.0] - 2026-08-25

### Security

- **Every registry write path was unauthenticated.** Both the compose and Helm
  nginx configurations allowed anonymous `PUT` and `DELETE` of package metadata
  *and* of artifacts at every path depth. Because `package-metadata.json`
  carries `fpm_path` and `checksum_sha256`, anyone able to reach the registry
  could repoint a package at an arbitrary artifact and forge its checksum,
  defeating the integrity chain end to end; an anonymous
  `PUT /metadata/index.json` could erase the catalogue.

  Three separate causes, all now fixed:
  - `limit_except` was commented out on the metadata location behind a stale
    TODO claiming the client did not yet support credentials. It has since
    v1.5.0.
  - `limit_except` does not inherit into nested locations, so the `\.fpm$`
    location had no authentication of its own.
  - The server-level regex intended to protect artifact paths was **dead
    code**: once a nested location matches, nginx returns from
    `ngx_http_core_find_location` and never evaluates server-level regex
    locations, and `location /` always matches first.

- Auth and CORS policy is now factored into `nginx/fpm-location.conf`, included
  by every location serving repository content. nginx refuses to start if it is
  missing, so the failure mode is closed.
- The Helm chart no longer defaults to `admin` / `adminpassword`; it fails to
  render unless credentials are supplied, and `NOTES.txt` no longer prints the
  password.
- `COPY` and `MOVE` removed from `dav_methods` — the client never issues them.

### Fixed

- CORS headers were silently absent from `/metadata/*.json` and `.fpm`
  responses. A location declaring any `add_header` discards every `add_header`
  inherited from its parent, so declaring `Content-Type` in those nested
  locations dropped the CORS headers a browser client needs.

- **`latest_version` was chosen by string comparison** (`cmd/publish.go`), so a
  repository that had published 1.10.0 kept reporting 1.9.0 as latest, and
  `fpm install <app>` with no pinned version installed the older package
  without saying so. Version ordering now lives in `internal/semver`, and the
  latest version is recomputed across every published version rather than
  compared against the stored one — which also repairs metadata the old
  comparison had already corrupted.

- **Packaging died on symlinks it could not follow.** frappe/wiki checks in
  `wiki/public/node_modules` as a symlink to an install-time artifact; staging
  followed the link, failed the stat, and the whole package aborted. Dangling
  and directory symlinks are now skipped with a warning; symlinks to regular
  files are still copied.

### Added

- **`fpm mirror`, a bulk builder for official Frappe apps.** Reads a curated
  CSV catalog (`catalog/apps.csv`, frappe-org repositories only — the loader
  rejects anything else), discovers the latest release tag of each major line
  with `git ls-remote` (no clone), skips versions the registry already has via
  the anonymous metadata read path, and packages and publishes only what is
  missing. Untagged apps can track a branch instead, published as a prerelease
  pseudo-version (`0.0.0-git.<date>.<sha>`) that can never become
  `latest_version`. Builds reuse a persistent cache across apps and runs:
  git checkouts under `~/.fpm/build-cache/src/`, `PIP_CACHE_DIR` for wheel
  vendoring, and npm/yarn caches for optional per-app asset-build scripts
  (`catalog/build/<slug>.sh`). Failures are isolated per app and reported
  (`--report` writes JSON; exit 1 when anything failed, 2 on config errors);
  a packaging failure with bundled wheels is retried without them and labeled
  `published-nodeps`. Replaces `scripts/bulk-package.py` and
  `scripts/apps.json`, which guessed artifact names from repo names and
  hardcoded versions.

- **`fpm-registry`, a registry service.** Replaces nginx's WebDAV on the *write*
  path only; reads remain static files under a document root, so an unmodified
  `fpm` client cannot tell the difference beyond the base URL. It exists
  because three properties could not be built on WebDAV at all:

  - **Per-organisation ownership.** A publisher token is scoped to the orgs it
    may write to, so a publisher can no longer overwrite `frappe/erpnext`.
  - **Real integrity.** The server hashes the bytes it receives and re-verifies
    the archive's own content checksum. The client-supplied
    `package-metadata.json` is still accepted, so `fpm publish` keeps working,
    but its `fpm_path` and `checksum_sha256` are discarded rather than stored.
  - **Atomic publishing.** Metadata and the catalogue index are derived from the
    packages that exist and written under one lock, so concurrent publishes no
    longer lose each other and a truncated index cannot persist.

  It also refuses republishing a version (409), refuses artifacts whose
  coordinates disagree with their manifest, and counts downloads — which the
  static registry had no way to observe.

  `fpm-registry serve` runs it; `fpm-registry issue` mints a publisher token,
  storing only its hash. `POST /admin/publishers` does the same over HTTP for
  an administrative credential, so publisher onboarding does not require shell
  access.

- `PackageVersionMetadata` now carries `frappe_compatibility`,
  `source_control_url`, `author`, `package_type` and `wheel_platform`, and
  dependencies are populated from the manifest instead of being dropped. All
  `omitempty`, so older clients are unaffected. This lets a consumer answer
  "which Frappe versions does this support?" from a JSON read instead of
  downloading and unpacking the artifact.

- `test/registry` — an acceptance suite that boots real nginx and asserts the
  write-protection and CORS contract, run against **both** the compose config
  and the Helm-rendered config so the two cannot drift apart again. Behaviour
  is specified in `test/registry/features/registry_auth.feature`. Run with
  `go test -tags integration ./test/registry/...`. The service's own contract is
  specified in `test/registry/features/registry_service.feature`.

### Planned Features
- Automatic directory creation for new packages
- TLS/SSL support out of the box
- Package signing and verification
- Repository mirroring and synchronization
- Web UI for repository browsing
- Package dependency graph visualization
- Integration with CI/CD platforms
- Docker registry-style API support
- Multi-repository package resolution
- Package version conflict detection
- Rollback and version pinning

## [2.0.0] - 2026-08-14

### Major Milestone
Version 2.0.0 marks a major production-ready release of the Frappe Package Manager (FPM) CLI and repository ecosystem, delivering offline air-gapped installation, end-to-end checksum verification, remote package discovery, secure HTTP basic authentication, and bench site integration.

### Added
- **Offline Installation & Bundled Python Wheels**: Self-contained packages bundle all Python dependencies from `requirements.txt` and `pyproject.toml` into `wheels/` for deterministic, zero-network installation.
- **Dependency Inspection (`fpm deps`)**: Inspect Python dependencies, build-system requirements, and bundled wheel platform targets directly from `.fpm` package archives or the local store.
- **Remote Package Search (`fpm search --remote`)**: Query configured repositories via a repository package index (`metadata/index.json`) for remote package discovery.
- **HTTP Basic Authentication**: Secure publishing and fetching against authenticated repositories using environment variables (`FPM_REPO_<NAME>_PASSWORD`, `FPM_REPO_PASSWORD`) or interactive prompts, with safe credential storage in `~/.fpm/config.json`.
- **Bench Site Installation (`fpm install --site <site>`)**: Direct bench site app installation executing `bench --site <site> install-app <app>`.
- **Version Reporting (`fpm --version`)**: Build-stamped version and commit output via `git describe`.
- **Repository Management (`fpm repo remove <name>`)**: Safely remove repositories and auto-clean default targets.

### Security
- **Mandatory SHA-256 Checksum Verification**: Fail-closed verification rejecting packages lacking valid checksums in repository metadata across fresh downloads and cache hits.
- **Scoped Credentials**: Repository authentication credentials strictly scoped to the repository host to prevent token leakage on redirects.

## [1.7.0] - 2026-08-12

### Added
- **`fpm --version`** reports the build it came from. The version and commit are stamped
  by the Makefile from `git describe`, so released binaries identify themselves; a build
  made with plain `go build` reports `dev` rather than claiming a release number it does
  not have.
- **`fpm repo remove <name>`** (alias `rm`) removes a configured repository.
  `config.RemoveRepository` had been implemented but wired to no command. Removing the
  repository that was the default publish target also unsets the default, which would
  otherwise fail later complaining about a repository the user thought was configured.
- **`fpm install --site` is implemented.** The flag was advertised in help text while the
  install printed `Placeholder: Next steps: Running migrations for a site`. It now runs
  `bench --site <site> install-app <app>`, delegating to bench because site installation
  touches the database and runs the app's own patches. Without `--site`, the install says
  plainly that the app is in the bench but not active on any site.

### Notes
- A site install that fails is reported as an error with bench's own output, since the app
  is in the bench but not active on the site, and that state needs to be visible.
- When a bench has no `env/bin/bench`, the error gives the command to run by hand rather
  than implying the whole install failed.

## [1.6.0] - 2026-08-12

### Security
- **A package whose repository metadata records no `checksum_sha256` is now rejected
  rather than accepted with a warning.** The leniency existed so that packages published
  before checksums were recorded stayed installable, but it meant any repository could
  skip verification entirely by omitting the field, while the client reported success
  having checked nothing — a weaker guarantee than performing no verification at all.
  The unverifiable artifact is removed rather than left in the cache, and the rejection
  applies to cache hits as well as fresh downloads, so it cannot be bypassed by planting
  a file where a download would land.

### Notes
- The error names the remedy: republish the package to record a checksum.
- `fpm publish` already refused to upload an archive with no `content_checksum`, so both
  ends of the chain now fail closed rather than open.

## [1.5.0] - 2026-08-12

### Added
- **Authentication for repositories requiring HTTP Basic Auth.** The bundled repository
  server requires authentication for every write, and the client sent credentials nowhere,
  so `fpm publish` could not succeed against the repository FPM itself ships. Repositories
  now take a `--username`, and every request to a configured repository carries
  credentials: publish, install, get-app and search.
- Passwords are resolved from `FPM_REPO_<NAME>_PASSWORD`, then `FPM_REPO_PASSWORD`, then
  an interactive no-echo prompt. Non-interactive use fails with guidance naming both
  variables rather than blocking on a prompt nothing will answer.
- A 401 or 403 now explains what to do — configure a username, or check the password
  source — instead of surfacing a bare status code.
- `fpm repo list` shows the username configured for each repository.

### Security
- **Passwords are never written to `~/.fpm/config.json`.** Only the username is stored;
  the file is plain text and commonly ends up in backups and dotfile repositories.
- Credentials are scoped to the repository host, so they are not forwarded when a
  repository redirects to a CDN or object store on another origin.

### Notes
- Repositories configured without a username are treated as public and receive no
  credentials, so existing setups are unaffected.
- During a multi-repository search or install, a repository whose credentials cannot be
  resolved is skipped with a warning rather than failing the whole operation, since
  another repository may serve the package.

## [1.4.0] - 2026-08-12

### Added
- **`fpm search --remote`** queries configured repositories, matching keywords against a
  new repository package index. Previously a search could only reach a repository when
  the query named an exact `<org>/<app>`, because per-package metadata lives at
  `metadata/<org>/<app>/package-metadata.json` and can only be fetched by a client that
  already knows both names. There was nothing to search.
- **Repository package index** at `metadata/index.json`, maintained by `fpm publish`.
  It catalogues every package in a repository with its latest version and description.
  A repository that publishes no index can still be queried for an exact `<org>/<app>`;
  it just cannot be searched by keyword.
- **`fpm deps` is implemented.** It previously printed its own name and the argument it
  was given. It now resolves a package — a path to an `.fpm`, or `<org>/<app>[==<version>]`
  from the local store — and reports the Python dependencies declared in the
  `requirements.txt` and `pyproject.toml` the package ships, together with the wheels it
  bundles and the platform they were built for. Reading the shipped manifests rather than
  the source tree means it describes what an install would actually resolve.

### Changed
- **`fpm search` no longer contacts repositories unless `--remote` is given.** It
  previously queried them automatically whenever the query looked like `<org>/<app>`,
  making network access a surprise. Local store and cache searches are unchanged.

### Notes
- `fpm deps` reports FPM-level package dependencies from `app_metadata.json` when present,
  labelled as not resolved during install, since dependency resolution is still unbuilt.
- A failure to update the repository index is a warning, not an error: the package itself
  has published successfully by that point, and a stale catalogue makes it harder to
  discover but not unusable.

## [1.3.0] - 2026-08-12

### Added
- **Offline installation via bundled Python dependencies.** `fpm package` now bundles the
  app's Python dependencies into a `wheels/` directory inside the package.
  `fpm install` pins pip to that directory (`--no-index --find-links`), so installing
  requires no network access and uses the exact dependency set that was bundled, rather
  than re-resolving `requirements.txt` at install time.
- Dependencies are read from both `requirements.txt` and `pyproject.toml`, so apps on
  either convention are handled without configuration; an app carrying both has its
  specifiers merged and de-duplicated. From `pyproject.toml`, both `[project].dependencies`
  and `[build-system].requires` are collected: `fpm install` performs a PEP 517 editable
  build on the target, so a build backend that was not bundled would make an offline
  install fail before it starts. Optional extras are not bundled.
- `--platform` selects the wheel platform tag to bundle for. Production packages default
  to `manylinux2014_x86_64`, since Frappe deployments target amd64 Linux and that is
  rarely the packaging machine; other package types build for the packaging host.
  Cross-target bundling uses `pip download --only-binary=:all:`, so a dependency with no
  wheel for that platform fails loudly instead of silently producing a host-tagged build.
- `wheel_platform` in `app_metadata.json` records what the bundled wheels were built for.
  `fpm install` warns when that does not match the installing host, surfacing the likely
  cause ahead of a confusing pip error.

### Changed
- **Production packages now bundle dependencies by default.** A production package is a
  deployment artifact, so it is self-contained unless told otherwise. Development packages
  do not bundle, since it only slows the local iteration loop. Either default can be
  overridden with `--bundle-deps` / `--bundle-deps=false`.

  This makes `python3` with `pip` a requirement for producing production packages, and
  packaging now fails when a dependency publishes no wheel for the target platform. That
  is deliberate: the alternative is a package that claims to be deployable but cannot
  install on the target. Use `--bundle-deps=false` to package without dependencies.

### Notes
- Installing is unchanged for packages that bundle no dependencies: they still resolve
  from the network.
- Bundled wheels are staged before the content checksum is calculated, so they are covered
  by the package's integrity hash like every other file. Tampering with a bundled wheel is
  caught by `fpm publish`.
- Node dependencies are deliberately not bundled. `node_modules` is a build-time
  requirement for producing JS/CSS; the built output travels in `compiled_assets/`, which
  `fpm install` deploys to `sites/assets/<app>/`, so no Node toolchain is needed on the
  target host.

## [1.2.0] - 2026-08-12

### Security
- **Package contents are now covered by the integrity checksum.** `content_checksum` was
  calculated before `requirements.txt`, `package.json`, `install_hooks.py`, and
  `compiled_assets/` were staged, so those files shipped inside the `.fpm` without being
  covered by the hash. `install_hooks.py` executes during installation, so a modified
  package could pass verification while carrying altered install-time code. The checksum
  is now calculated over the fully staged payload.
- **`fpm publish` verifies package contents before uploading.** The archive's payload is
  re-hashed and compared against the `content_checksum` recorded inside it. A mismatch is
  a hard error, so a package modified after it was built never reaches a repository.
  Verification runs before the upload rather than after it.
- **Downloads are verified against the checksum in repository metadata.** `fpm install`
  and `fpm get-app` previously carried `checksum_sha256` around without ever checking it.
  Downloads that do not match are now rejected, the offending file is deleted, and
  resolution falls through to the next configured repository.
- **Cached packages are re-verified before reuse.** A cache hit previously returned
  whatever was on disk unchecked. A poisoned or corrupted cache entry is now discarded and
  re-downloaded instead of being installed.

### Fixed
- `fpm publish` no longer emits a spurious checksum warning on every publish. It compared
  `content_checksum` (a hash of the extracted payload) against a hash of the `.fpm` bytes —
  two values describing different inputs, which could never be equal. The two checksums are
  now documented and used for their separate purposes.

### Added
- `archive.CalculateArchiveContentChecksum` / `archive.VerifyArchiveContentChecksum` for
  verifying a `.fpm`'s payload directly from its entries, without extracting it.
- Test coverage for `internal/repository`, which previously had none, covering download
  verification, cache poisoning, and missing-checksum handling.

### Notes
- Packages whose repository metadata records no `checksum_sha256` cannot be verified. FPM
  warns on stderr and proceeds, so packages published before checksums were recorded remain
  installable. (Tightened to a hard rejection in 1.6.0.)
- A repository serving artifacts that do not match its own published metadata will now be
  rejected rather than silently trusted. Republishing such packages regenerates correct
  metadata.

## [1.1.1] - 2026-01-22

### Fixed
- Made `BINARY_NAME` and `BUILD_DIR` overridable in the `Makefile` (`?=` instead of `=`),
  so `make BINARY_NAME=... BUILD_DIR=...` works for custom and cross-compiled builds.

## [1.1.0] - 2026-01-22

### Added
- **Standardized Build System**: Added a `Makefile` for consistent builds, tests, formatting, and cross-compilation.
- **Dynamic Vyogo Branding**: Integrated `go-figure` for professional "Vyogo FPM" ASCII art in the CLI.
- **Improved CI/CD**:
  - Enforced code formatting and simplification (`gofmt -s`) in the pipeline.
  - Enhanced unit test reporting with `gotestsum`.
  - Standardized multi-arch binary releases (Linux, macOS, Windows) using the Makefile.

### Fixed
- Fixed all pending unit test failures across the CLI command packages.
- Resolved compilation issues in `internal/repository` and `internal/config`.
- Improved search logic to correctly handle `<org>/<app>` identifiers in local and cache searches.
- Fixed environment variable precedence for `FPM_APPS_BASE_PATH`.

## [1.0.0] - 2025-11-25

### Added
- **FPM CLI Tool**: Command-line interface for managing Frappe packages
  - `fpm package`: Package Frappe applications into `.fpm` files
  - `fpm install`: Install packages into Frappe benches
  - `fpm publish`: Publish packages to repositories
  - `fpm repo add/list/default`: Repository management
  - `fpm search`: Search for packages across repositories
  - `fpm get-app`: Download packages from repositories
  - `fpm deps`: Inspect package dependencies

- **FPM Repository Server**: Enterprise-grade package repository deployment
  - Nginx with WebDAV support for package storage
  - HTTP Basic Authentication for write operations
  - Public read access for package downloads
  - Podman/Docker Compose deployment configuration
  - Automated setup scripts for easy deployment
  - Health check endpoint for monitoring
  - Support for large file uploads (up to 500MB)

- **Package Format**: Standardized `.fpm` package format
  - ZIP-based archive structure
  - Embedded metadata (app_metadata.json)
  - SHA256 checksums for integrity verification
  - Support for dependencies and version constraints
  - Frappe application structure preservation

- **Repository Features**:
  - Hierarchical package organization: `/<org>/<appName>/<version>/`
  - Centralized metadata: `/metadata/<org>/<appName>/package-metadata.json`
  - Version discovery and "latest" resolution
  - Package search across multiple repositories
  - Repository prioritization

- **Documentation**:
  - Comprehensive deployment guide (`fpm-repo-README.md`)
  - Quick start guide (`QUICK_START.md`)
  - Vision document (`VISION.md`)
  - Deployment test results and known issues
  - Step-by-step developer guide

- **CI/CD**:
  - GitHub Actions workflow for automated testing
  - Multi-platform release builds (Linux, macOS, Windows on AMD64/ARM64)
  - Automated release creation with binaries and deployment packages
  - SHA256 checksum generation for all artifacts

### Fixed
- HTTP 204 (No Content) status code now recognized as success for WebDAV operations
- Nginx WebDAV configuration compatible with nginx:alpine image
- Directory permissions and ownership for package uploads
- ARM64/Apple Silicon build support

### Security
- HTTP Basic Authentication for repository write operations
- Bcrypt password hashing in `.htpasswd`
- `.gitignore` configured to exclude sensitive files
- Public read access for package downloads (configurable)

### Documentation
- Complete API reference for all CLI commands
- Repository setup and configuration guide
- Production deployment recommendations
- Security best practices
- Backup and restore procedures
- Troubleshooting guide

### Known Limitations
- Nginx requires pre-created directories for new org/app combinations
- No TLS/SSL by default (HTTP only, can be configured)
- Metadata endpoint temporarily has no authentication for testing

[Unreleased]: https://github.com/vyogotech/fpm/compare/v2.2.0...HEAD
[2.2.0]: https://github.com/vyogotech/fpm/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/vyogotech/fpm/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/vyogotech/fpm/compare/v1.7.0...v2.0.0
[1.7.0]: https://github.com/vyogotech/fpm/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/vyogotech/fpm/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/vyogotech/fpm/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/vyogotech/fpm/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/vyogotech/fpm/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/vyogotech/fpm/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/vyogotech/fpm/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/vyogotech/fpm/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/vyogotech/fpm/releases/tag/v1.0.0
