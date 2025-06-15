# Frappe Package Manager (FPM) Vision Document

## Executive Summary

FPM aims to revolutionize Frappe application deployment by introducing a robust package management system that eliminates Git dependencies in production environments. FPM will provide Maven-like capabilities for the Frappe ecosystem, enabling reproducible builds, offline deployments, and enterprise-grade dependency management.

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

## Package Search

### `fpm search [query]`

Searches for FPM packages by matching the query against the `groupID`, `artifactID`, or `description` of packages. The search is performed on the metadata that FPM has locally cached from configured repositories.

*   `[query]`: (Optional) The search term. If omitted, `fpm search` lists all packages found in the local metadata cache. The search is case-insensitive.

**Example:**
```bash
# List all cached package metadata
fpm search

# Search for packages related to "erp"
fpm search erp
```
**Example Output:**
```
REPOSITORY           PACKAGE (GROUP/ARTIFACT)                   LATEST_VER      DESCRIPTION
-------------------- ---------------------------------------- --------------- ---------------------------------------------
central              frappe/erpnext                           13.20.1         ERPNext is the world's best free and open source ERP
community            customorg/custom_erp_module              1.0.5           Adds custom features to ERPNext for ACME Corp
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
