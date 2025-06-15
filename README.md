# Frappe Package Manager (FPM) Vision Document

## Executive Summary

FPM aims to revolutionize Frappe application deployment by introducing a robust package management system that eliminates Git dependencies in production environments. FPM will provide Maven-like capabilities for the Frappe ecosystem, enabling reproducible builds, offline deployments, and enterprise-grade dependency management.

## Overview

FPM (Frappe Package Manager) is a command-line interface (CLI) tool designed to simplify the packaging, distribution, and installation of Frappe applications. It introduces a standardized `.fpm` package format and supports fetching packages from configured repositories, similar to package managers in other ecosystems like npm or Maven.

A key concept in FPM is the **local FPM application store**, typically located at `~/.fpm/apps/`. When packages are created with `fpm package` (by default) or installed with `fpm install`, the application's contents along with its original `_*.fpm` package file are stored here, organized by `/<org>/<appName>/<version>/`. This local store serves as the primary source for FPM when installing packages into a Frappe bench, before attempting to download from remote repositories.

## Vision Statement

To transform Frappe application deployment from a Git-centric development model into an enterprise-ready package management system that supports both traditional development workflows and modern DevOps practices.

## Core Principles

1. **Simplicity**: Single binary CLI tool that "just works"
2. **Reliability**: Reproducible, version-specific deployments
3. **Flexibility**: Support for multiple repositories and deployment scenarios
4. **Enterprise-Ready**: Security, authentication, and offline capabilities
5. **Ecosystem Compatibility**: Seamless integration with existing Frappe workflows

## System Architecture

### Component Overview

1. **FPM CLI** (Go binary)
   - Package creation, installation, and repository management
   - Dependency resolution and validation
   - Configuration management

2. **Package Format** (.fpm files)
   - Self-contained ZIP archives with standardized structure
   - Metadata including dependencies, version constraints, and compatibility
   - Pre-built assets and installation hooks

3. **Repository System**
   - Multiple repository support (local, corporate, central, community)
   - Repository prioritization and fallback mechanisms
   - Standardized repository API with multiple implementations

### Package Structure

The `.fpm` file is a ZIP archive containing the application code and necessary metadata. The structure is as follows:

```
app-name-1.0.0.fpm
├── app_metadata.json       # Core metadata: name, version, org, dependencies, checksums, etc.
├── app_name/               # The main application module directory (e.g., my_app/)
│   ├── __init__.py
│   ├── hooks.py
│   └── ... (other app source files and directories)
├── assets/                 # Optional: other assets at the root of the app source (e.g. UI assets not in public/)
├── public/                 # Optional: public assets (if not within app_name/public/)
├── requirements.txt        # Optional: Python dependencies
├── package.json            # Optional: Node dependencies (if applicable)
├── install_hooks.py        # Optional: Custom hooks executed during fpm install (not bench install)
└── compiled_assets/        # Optional: Pre-compiled client-side assets (e.g., from frappe build)
    └── ...
```
The key change from earlier designs is the removal of the `app_source/` directory; the application module and other standard files are now at the root of the archive.


### Repository Layout

/{groupId}/{artifactId}/{version}/{artifactId}-{version}.fpm /metadata/{groupId}/{artifactId}/package-metadata.json
Examples: /frappe/erpnext/13.0.1/erpnext-13.0.1.fpm /company/custom-app/1.0.0/custom-app-1.0.0.fpm


## Key Workflows

### Development Workflow

1. Develop Frappe app using traditional Git-based approach
2. Create package from local development directory
3. Test package locally or publish to development repository
4. Iterate on development

```bash
cd ~/frappe-bench/apps/my-custom-app
# Make code changes as normal

# Package the app. This creates my-custom-app-1.2.3.fpm
# and by default installs it to the local FPM app store (~/.fpm/apps).
fpm package --version 1.2.3 --org myorg
# Or, to skip installation to the local FPM store:
fpm package --version 1.2.3 --org myorg --skip-local-install

# Install the packaged app (from local file or FPM store) to a specific bench and site
fpm install ./my-custom-app-1.2.3.fpm --bench-path /path/to/bench --site mysite.local
# Or, if it was installed to the local store by `fpm package`:
fpm install myorg/my-custom-app==1.2.3 --bench-path /path/to/bench --site mysite.local
```

### Production Deployment Workflow

1.  **Package or Download**:
    *   On a build server or development machine, package your applications:
        ```bash
        # Assuming current directory is the app source (e.g., ./erpnext)
        fpm package --version 13.0.1 --org frappe
        # This creates erpnext-13.0.1.fpm and installs it to the local FPM store.
        # You can then copy/upload erpnext-13.0.1.fpm to your production server.
        # Alternatively, publish it to a private FPM repository (see Repository Management).
        ```
    *   Or, if packages are hosted on an FPM repository, they can be directly installed on the production server.

2.  **Deploy to Production Server**:
    *   Ensure FPM is configured on the production server (see Repository Management if using remote repos).
    *   Install the package:
        ```bash
        # If installing from a local .fpm file copied to the server:
        fpm install ./erpnext-13.0.1.fpm --bench-path /path/to/production-bench --site production.site

        # If installing from a configured FPM repository:
        fpm install frappe/erpnext==13.0.1 --bench-path /path/to/production-bench --site production.site
        # FPM will first check the local FPM app store (~/.fpm/apps), then configured repositories.
        ```
    *   FPM handles placing the app in its local store and symlinking it to the bench.

## Repository Management

FPM allows you to configure multiple package repositories. It will search these repositories for packages based on priority.

### `fpm repo add <name> <url> [--priority <number>]`

Adds a new FPM package repository to your local FPM configuration.

*   `<name>`: A unique, user-defined name for the repository (e.g., `mycorp-internal`, `community-main`).
*   `<url>`: The base URL of the FPM repository.
*   `--priority <number>`: (Optional) Sets the priority for the repository. Lower numbers indicate higher priority. Defaults to `0`. Repositories with the same priority are typically searched in the order they were added or by name.

**Example:**
```bash
fpm repo add central https://fpm.mycompany.com/repo --priority 10
fpm repo add community https://community.fpm.io/repo --priority 20
fpm repo add local-dev file:///path/to/local/fpm-repo --priority 0
```

### `fpm repo list`

Lists all configured FPM repositories, sorted by priority (lower number first, then by name).

**Example Output:**
```
NAME                 URL                                                PRIORITY
----                 ---                                                --------
local-dev            file:///path/to/local/fpm-repo                     0
central              https://fpm.mycompany.com/repo                     10
community            https://community.fpm.io/repo                      20
```

*(Future commands: `fpm repo remove <name>` and `fpm repo update <name | --all>`).*

### `fpm repo default [repo_name]`

Sets or shows the default FPM repository to be used for `fpm publish` operations when no `--repo` flag is specified.

*   `[repo_name]`: (Optional) The name of a configured repository to set as the default.
    *   If provided, FPM will set this repository as the default for publishing. The repository must already exist in the configuration.
    *   If omitted, FPM will display the currently configured default publish repository, or indicate if none is set.

**Examples:**
```bash
# Set 'mycorp-releases' as the default publish repository
fpm repo default mycorp-releases

# Show the current default publish repository
fpm repo default
```

## Package Search

### `fpm search [query]`

Searches for FPM packages by matching the query against the `groupID`, `artifactID`, or `description`. The search prioritizes results from different sources in the following order:
1.  **Local FPM App Store (`~/.fpm/apps/`)**: Shows packages already installed locally by FPM. These are considered the highest priority.
2.  **Live Remote Repositories**: If the `[query]` is a specific package identifier in the format `<group>/<artifact>` (without version or wildcards), FPM will query configured remote repositories live for this specific package.
3.  **Locally Cached Repository Metadata (`~/.fpm/cache/`)**: Shows packages known from the last time repository metadata was fetched/updated.

*   `[query]`: (Optional) The search term.
    *   If omitted, `fpm search` lists all packages found in the local FPM app store and the local metadata cache (it does not perform live remote queries for an empty query).
    *   If a generic keyword (e.g., "erp"), it searches package identifiers and descriptions in the local store and cache.
    *   If a specific identifier (`<group>/<artifact>`), it additionally performs live queries to remote repositories for that exact package.
    *   The search is case-insensitive.

**Output:**
The output includes a "SOURCE" column to indicate where the package information was found:
*   `(local-store)`: The package (specific version) is installed in your local FPM app store.
*   `(remote: <repo_name>)`: The package version information was fetched live from the named remote repository.
*   `(cache: <repo_name>)`: The package information is from the local cache of metadata for the named remote repository.

If a package version is found in multiple sources, the result with the highest priority (local-store > remote-live > cache) is displayed. The command lists specific versions found, not just a "latest version" summary.

**Example:**
```bash
# List all packages found in local store and cache
fpm search

# Search for packages related to "erp" in local store and cache
fpm search erp

# Search for a specific package "myorg/myerp" across local store, cache, and live remotes
fpm search myorg/myerp
```
**Example Output (illustrative):**
```
SOURCE                PACKAGE (GROUP/ARTIFACT)                   VERSION         DESCRIPTION
--------------------  ---------------------------------------- --------------- ---------------------------------------------
(local-store)         myorg/myerp                              1.0.1           My Custom ERP Module (Installed)
(remote: central)     myorg/myerp                              1.0.0           My Custom ERP Module
(cache: community)    frappe/erpnext                           13.20.1         ERPNext is the world's best free and open source ERP
```

## Hosting FPM Repositories

An FPM repository is a web server (or local directory structure) that serves package files and metadata according to a defined layout.

*   **Package Files**: Stored at a path like `/<groupID>/<artifactID>/<version>/<artifactID>-<version>.fpm`.
    *   Example: `frappe/erpnext/13.0.1/erpnext-13.0.1.fpm`
*   **Metadata File**: A single JSON file per package (group/artifact combination) provides information about all available versions.
    *   Path: `/metadata/<groupID>/<artifactID>/package-metadata.json`
    *   Example: `/metadata/frappe/erpnext/package-metadata.json`
    *   **Schema for `package-metadata.json`**:
        ```json
        {
          "groupId": "frappe",
          "artifactId": "erpnext",
          "description": "Open Source ERP",
          "latest_version": "13.20.1",
          "versions": {
            "13.20.1": {
              "fpm_path": "frappe/erpnext/13.20.1/erpnext-13.20.1.fpm",
              "checksum_sha256": "sha256_checksum_of_the_fpm_file",
              "release_date": "YYYY-MM-DD",
              "dependencies": [
                // {"groupId": "frappe", "artifactId": "frappe", "version_constraint": ">=13.20.0,<14.0.0"}
              ],
              "notes": "Release notes for v13.20.1"
            },
            "13.19.0": {
              // ... metadata for other versions ...
            }
          }
        }
        ```

FPM clients use this metadata to find available packages and their download URLs. The `fpm_path` in `package-metadata.json` is relative to the repository's base URL.

To support `fpm publish`, a repository server must be able to:
1.  Receive `.fpm` package files via HTTP PUT requests at the defined package file path (e.g., `/<groupID>/<artifactID>/<version>/<artifactID>-<version>.fpm`).
2.  Receive `package-metadata.json` files via HTTP PUT requests at the defined metadata path (e.g., `/metadata/<groupID>/<artifactID>/package-metadata.json`).
This can be a simple static file server with PUT-to-create/update capabilities or a more sophisticated package registry application.

## Publishing Packages

### `fpm publish [<group>/<artifact>[==<version>]] [--from-file <filepath>] [--repo <repo_name>]`

Publishes a Frappe application package to a configured FPM repository.

The command can publish a package in two ways:
1.  **From the local FPM app store**: By specifying the package identifier `[<group>/<artifact>[==<version>]]`.
    *   If the version is omitted or specified as "latest", FPM resolves the lexicographically latest version found in the local store (`~/.fpm/apps/<group>/<artifact>/*`).
    *   It looks for the corresponding `_*.fpm` file within the resolved version's directory in the store.
2.  **From a direct `.fpm` file**: By using the `--from-file <filepath>` flag.

**Arguments & Flags:**
*   `[<group>/<artifact>[==<version>]]`: (Optional) The identifier of the package in the local FPM app store.
*   `--from-file <filepath>`: (Optional) Path to the `.fpm` package file to publish directly.
*   `--repo <repo_name>`: (Optional) The name of the configured repository to publish to. If not specified, FPM will use the default publish repository set by `fpm repo default <repo_name>`.

**Publishing Process:**
1.  The specified `.fpm` file (either from the local store or via `--from-file`) is located.
2.  Metadata (`app_metadata.json`) is read from this `.fpm` file to determine `Org`, `AppName`, and `PackageVersion`.
3.  The target repository is determined (via `--repo` or default).
4.  The existing `package-metadata.json` for the app (`<org>/<appName>`) is fetched from the remote repository. If it doesn't exist, new metadata is initialized.
5.  The command checks if the version being published already exists in the remote metadata. If so, it currently errors out (a `--force` flag might be added in the future).
6.  The `.fpm` package file is uploaded to the repository (typically via HTTP PUT) to its structured path (e.g., `/<org>/<appName>/<version>/<appName>-<version>.fpm`).
7.  The SHA256 checksum of the local `.fpm` file is calculated.
8.  A new version entry is added/updated in the `PackageMetadata` structure (including the FPM path on the server, checksum, and release date).
9.  The `LatestVersion` field in the `PackageMetadata` is updated if the current version is newer (currently uses lexicographical comparison; TODO: SemVer).
10. The updated `package-metadata.json` is uploaded back to the repository (typically via HTTP PUT).

**Examples:**

```bash
# Publish version 1.2.3 of myorg/my-app from the local FPM store to 'mycorp-releases' repo
fpm publish myorg/my-app==1.2.3 --repo mycorp-releases

# Publish the latest version of myorg/my-app found in the local store to the default repo
fpm publish myorg/my-app

# Publish directly from a .fpm file to the 'mycorp-releases' repo
fpm publish --from-file ./my-app-1.2.4.fpm --repo mycorp-releases
```
