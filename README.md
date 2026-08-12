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
- `--platform`: Wheel platform tag to bundle for (default: `manylinux2014_x86_64` for prod, packaging host otherwise)

#### `fpm install`
Install a Frappe application package.

```bash
fpm install <org>/<app>==<version> --bench-path /path/to/bench [--site mysite.local]
```

#### `fpm publish`
Publish a package to a repository.

```bash
fpm publish <org>/<app>==<version> [--repo repository-name]
fpm publish --from-file ./my-app-1.0.0.fpm --repo repository-name
```

### Repository Management

#### `fpm repo add`
Add a new FPM package repository.

```bash
fpm repo add <name> <url> [--priority <number>]
```

**Example:**
```bash
fpm repo add company-repo https://fpm.company.com --priority 10
fpm repo add local-dev http://localhost:8080 --priority 1
```

#### `fpm repo list`
List all configured repositories.

```bash
fpm repo list
```

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
Inspect the dependencies a package declares and bundles.

```bash
fpm deps ./my-app-1.0.0.fpm      # a package file
fpm deps myorg/my-app            # latest in the local store
fpm deps myorg/my-app==1.0.0     # a specific version
```

Reports the Python dependencies declared in the `requirements.txt` and `pyproject.toml`
the package ships, and whether it bundles wheels for offline installation — so you can
tell before installing whether it will reach the network.

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
├── app_metadata.json         # Metadata: name, version, dependencies
├── my_app/                   # Main application module
│   ├── __init__.py
│   ├── hooks.py
│   └── ...
├── requirements.txt          # Python dependencies (either or both)
├── pyproject.toml            # PEP 621 project metadata
├── package.json              # Node dependencies (optional)
├── wheels/                   # Bundled Python deps (prod default)
├── compiled_assets/          # Prebuilt JS/CSS (optional)
└── assets/                   # Additional assets
```

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

# explicit target platform
fpm package -v 1.0.0 --platform macosx_11_0_arm64
```

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

The platform the wheels were built for is recorded as `wheel_platform` in
`app_metadata.json`, and `fpm install` warns when it does not match the installing host.
Vendored wheels are covered by the package's `content_checksum` like any other file.

> **Node dependencies are not vendored, and do not need to be.** `node_modules` is a
> build-time requirement for producing JS/CSS; ship the built output in `compiled_assets/`
> instead, which `fpm install` deploys to `sites/assets/<app>/`. No Node toolchain is
> needed on the target machine.

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

A package whose repository metadata records no `checksum_sha256` cannot be verified. FPM warns on stderr and proceeds, so that packages published before checksums were recorded remain installable.

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
