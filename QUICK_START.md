# FPM Repository - Quick Start Guide

## What Was Created

A production-ready FPM (Frappe Package Manager) repository using **Nginx + WebDAV** with **Podman Compose**, successfully tested with package publish and download operations.

## Start the Repository (Quick)

```bash
# 1. Create admin user credentials
htpasswd -cb nginx/.htpasswd admin admin123

# 2. Copy environment template
cp .env.example .env

# 3. Start the repository
podman compose up -d

# 4. Verify it's running
curl http://localhost:8080/health
# Should return: healthy
```

## Using the Repository

### Configure FPM Client

```bash
# Add repository
fpm repo add my-repo http://localhost:8080 --priority 1

# Set as default
fpm repo default my-repo

# List repositories
fpm repo list
```

### Publish a Package

```bash
# From your Frappe app directory
cd /path/to/your-app

# Package it
fpm package --version 1.0.0 --org myorg --app-name my_app

# Publish to repository
fpm publish myorg/my_app==1.0.0
```

### Download a Package

```bash
# Search for packages
fpm search myorg/my_app

# Download from repository
fpm get-app my-repo/myorg/my_app:1.0.0
```

## Testing with ERPNext

```bash
# Clone ERPNext (if you have access)
git clone https://github.com/frappe/erpnext.git
cd erpnext

# Ensure modules.txt exists and hooks.py is properly configured
# Package it
fpm package --version 15.0.0 --org frappe --app-name erpnext

# Publish
fpm publish frappe/erpnext==15.0.0

# Verify
curl http://localhost:8080/metadata/frappe/erpnext/package-metadata.json
```

## Repository URLs

Once running, your repository provides:

- **Health Check**: http://localhost:8080/health
- **Browse Packages**: http://localhost:8080/
- **Package Files**: http://localhost:8080/{org}/{app}/{version}/{app}-{version}.fpm
- **Metadata**: http://localhost:8080/metadata/{org}/{app}/package-metadata.json

## Important Notes

### 1. HTTP 204 Status Code Fix
We fixed a bug in FPM where it didn't recognize HTTP 204 (No Content) as a success status. The fix is in `internal/repository/remote.go` and needs to be in your FPM build.

### 2. Directory Creation
For the first publish of a new org/app, you may need to pre-create directories:

```bash
# Inside container
podman exec fpm-repository mkdir -p /var/fpm-repo/{org}/{app}/{version}
podman exec fpm-repository mkdir -p /var/fpm-repo/metadata/{org}/{app}
podman exec fpm-repository chown -R nginx:nginx /var/fpm-repo
```

### 3. Authentication (Current Limitation)
FPM doesn't yet support repository authentication in the config. For now, metadata writes are open (testing only). For production, you'll need to add auth support to FPM or protect the repository at the network level.

## Stopping/Managing

```bash
# Stop repository
podman compose down

# View logs
podman compose logs -f fpm-repo

# Restart
podman compose restart fpm-repo

# Check status
podman compose ps
```

## Files Reference

- `compose.yml` - Podman compose configuration
- `nginx/nginx.conf` - Nginx + WebDAV configuration  
- `scripts/setup.sh` - Automated setup script
- `scripts/add-user.sh` - Add more users
- `fpm-repo-README.md` - Full documentation
- `DEPLOYMENT_TEST_RESULTS.md` - Test results and findings

## Default Credentials

- **Username**: admin
- **Password**: admin123 (change this!)

## Backup Your Data

```bash
# Backup volume
podman volume export fpm_fpm-repo-data > fpm-repo-backup.tar

# Restore volume  
podman volume import fpm_fpm-repo-data < fpm-repo-backup.tar
```

## Production Checklist

Before deploying to production:

- [ ] Change default admin password
- [ ] Enable TLS/SSL (add certificates to nginx.conf)
- [ ] Re-enable authentication for metadata endpoint
- [ ] Set up regular backups
- [ ] Configure monitoring (optional: Prometheus/Grafana)
- [ ] Review and adjust resource limits in compose.yml
- [ ] Use a proper domain name instead of localhost
- [ ] Set up firewall rules
- [ ] Review nginx access logs regularly

## Getting Help

- See `fpm-repo-README.md` for comprehensive documentation
- See `DEPLOYMENT_TEST_RESULTS.md` for test results and known issues
- Check `podman compose logs fpm-repo` for errors

## Architecture

```
┌─────────────────────────────────────┐
│   FPM Client (Go CLI)              │
│   - Package creation                │
│   - Publishing                      │
│   - Download                        │
└──────────────┬──────────────────────┘
               │ HTTP/HTTPS
               ▼
┌─────────────────────────────────────┐
│   Nginx + WebDAV                   │
│   - HTTP Basic Auth                 │
│   - GET (public)                    │
│   - PUT/DELETE (authenticated)      │
└──────────────┬──────────────────────┘
               │
               ▼
┌─────────────────────────────────────┐
│   Persistent Volume                 │
│   /var/fpm-repo/                    │
│   ├── {org}/{app}/{version}/*.fpm   │
│   └── metadata/{org}/{app}/*.json   │
└─────────────────────────────────────┘
```

## Success! 🎉

Your FPM repository is now operational and ready for:
- Development testing
- CI/CD integration  
- Enterprise deployment (with security enhancements)


