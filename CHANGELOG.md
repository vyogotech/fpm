# Changelog

All notable changes to the Frappe Package Manager (FPM) project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned Features
- Authentication support in FPM client (username/password in repository config)
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
  installable.
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
- FPM client does not yet support authentication credentials in repository configuration
- Nginx requires pre-created directories for new org/app combinations
- No TLS/SSL by default (HTTP only, can be configured)
- Metadata endpoint temporarily has no authentication for testing

[Unreleased]: https://github.com/vyogotech/fpm/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/vyogotech/fpm/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/vyogotech/fpm/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/vyogotech/fpm/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/vyogotech/fpm/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/vyogotech/fpm/releases/tag/v1.0.0
