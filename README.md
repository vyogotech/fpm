# Frappe Package Manager (FPM)

[![CI](https://github.com/yourusername/fpm/actions/workflows/ci.yml/badge.svg)](https://github.com/yourusername/fpm/actions/workflows/ci.yml)
[![Release](https://github.com/yourusername/fpm/actions/workflows/release.yml/badge.svg)](https://github.com/yourusername/fpm/actions/workflows/release.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/yourusername/fpm)](https://golang.org/doc/devel/release.html)
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

Download the latest release for your platform from [Releases](https://github.com/yourusername/fpm/releases):

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
fpm search                    # List all packages
fpm search erp                # Search by keyword
fpm search myorg/my-app       # Search specific package
```

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
Inspect package dependencies.

```bash
fpm deps <package-path>
```

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
├── requirements.txt          # Python dependencies
├── package.json              # Node dependencies (optional)
└── assets/                   # Additional assets
```

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
git clone https://github.com/yourusername/fpm.git
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

- **Issues**: [GitHub Issues](https://github.com/yourusername/fpm/issues)
- **Discussions**: [GitHub Discussions](https://github.com/yourusername/fpm/discussions)
- **Documentation**: [Project Wiki](https://github.com/yourusername/fpm/wiki)

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
