# Changelog

All notable changes to the Frappe Package Manager (FPM) project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/vyogotech/fpm/compare/v2.0.0...HEAD
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
