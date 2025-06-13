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

app-name-1.0.0.fpm (ZIP archive) 
   ├── app_metadata.json 
   ├── app_source/ 
   │ ├── app_name/ 
   │ └── ... (app source files) 
   ├── compiled_assets/ │ 
   ├── js/ 
   │ └── css/ 
   ├── requirements.txt 
   ├── package.json 
   └── install_hooks.py


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
fpm package --version 1.2.3
fpm install ./my-custom-app-1.2.3.fpm --site mysite.local
```
Production Deployment Workflow
1. Create or download versioned packages
2. Deploy to production using offline packages
3. Install with dependency resolution

# On build server
fpm package --source ./erpnext --version 13.0.1
fpm publish erpnext-13.0.1.fpm --repo corporate

# On production server
fpm install erpnext==13.0.1 --site production.site

### Multi-Repository Workflow

1. Configure multiple repositories with priorities
2. Install packages with automatic repository selection
3. Resolve dependencies across repositories

```bash
# Configure repositories
fpm repo add corporate https://nexus.company.com/repository/frappe-packages
fpm repo add local file://~/.fpm/repository

# Install with automatic repository selection
fpm install custom-app==1.0.0 --site mysite

# View dependency resolution across repositories
fpm deps custom-app==1.0.0 --tree
```

