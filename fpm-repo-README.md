# FPM Repository - Deployment Guide

Enterprise-grade Frappe Package Manager (FPM) repository using Nginx with WebDAV support and HTTP Basic Authentication.

## Overview

This setup provides a production-ready FPM repository that supports:
- Package storage with proper organization (`/<org>/<appName>/<version>/`)
- Metadata management (`/metadata/<org>/<appName>/package-metadata.json`)
- HTTP Basic Authentication for write operations (PUT, DELETE)
- Public read access (GET) for downloading packages
- WebDAV support for file uploads
- Large file support (up to 500MB)
- Health check endpoint

## Architecture

```
FPM Repository Stack
├── Nginx (Alpine-based)
│   ├── WebDAV enabled
│   ├── Basic Authentication
│   └── Custom MIME types for .fpm files
├── Persistent Volume (fpm-repo-data)
│   ├── /packages - Package storage
│   └── /metadata - Package metadata
└── Configuration
    ├── nginx.conf - Nginx configuration
    └── .htpasswd - User credentials
```

## Prerequisites

- Podman or Docker installed
- podman-compose or docker compose
- apache2-utils (for htpasswd command)
  - Ubuntu/Debian: `sudo apt-get install apache2-utils`
  - RHEL/CentOS: `sudo dnf install httpd-tools`
  - macOS: `brew install httpd`

## Quick Start

### 1. Initial Setup

Run the setup script to create the authentication file:

```bash
./scripts/setup.sh
```

This will:
- Check for required dependencies
- Create `.htpasswd` file with an admin user
- Create `.env` file from template
- Display next steps

### 2. Start the Repository

```bash
# Using Podman
podman-compose up -d

# Using Docker
docker compose up -d
```

### 3. Verify Deployment

```bash
# Check health endpoint
curl http://localhost:8080/health

# View logs
podman-compose logs -f fpm-repo

# Check status
podman-compose ps
```

## Configuration

### Environment Variables

Edit `.env` file to customize:

```bash
# Port to expose the repository
FPM_REPO_PORT=8080
```

### User Management

#### Add a New User

```bash
./scripts/add-user.sh username
```

Or without the script:

```bash
htpasswd nginx/.htpasswd newuser
podman-compose restart fpm-repo
```

#### Remove a User

```bash
htpasswd -D nginx/.htpasswd username
podman-compose restart fpm-repo
```

## Using the Repository with FPM CLI

### Configure FPM Client

Add the repository to your FPM configuration:

```bash
# Add repository
fpm repo add mycompany-repo http://localhost:8080 --priority 10

# Set as default for publishing
fpm repo default mycompany-repo

# List configured repositories
fpm repo list
```

### Package and Publish

```bash
# Navigate to your Frappe app directory
cd /path/to/your/frappe-app

# Package the app
fpm package --version 1.0.0 --org myorg

# Publish to repository
fpm publish myorg/my-app==1.0.0 --repo mycompany-repo
```

### Install Packages

```bash
# Search for packages
fpm search myorg/my-app

# Install a package
fpm install myorg/my-app==1.0.0 --bench-path /path/to/bench
```

## Repository Structure

The repository follows the FPM standard layout:

```
/var/fpm-repo/
├── org1/
│   └── app1/
│       ├── 1.0.0/
│       │   └── app1-1.0.0.fpm
│       └── 1.0.1/
│           └── app1-1.0.1.fpm
├── org2/
│   └── app2/
│       └── 2.0.0/
│           └── app2-2.0.0.fpm
└── metadata/
    ├── org1/
    │   └── app1/
    │       └── package-metadata.json
    └── org2/
        └── app2/
            └── package-metadata.json
```

## Security Considerations

### Authentication

- **Read operations (GET)**: Public access (no authentication required)
- **Write operations (PUT, DELETE, MKCOL)**: Requires HTTP Basic Authentication
- Credentials are stored in `/nginx/.htpasswd` (bcrypt hashed)

### Production Recommendations

1. **Use HTTPS**: Add SSL/TLS termination (use a reverse proxy like Traefik or configure Nginx with SSL)
2. **Firewall**: Restrict access to trusted networks
3. **Backup**: Regular backups of the `fpm-repo-data` volume
4. **Monitoring**: Add monitoring for disk space and access logs
5. **Strong Passwords**: Use strong passwords for all users

## Advanced Configuration

### Enable HTTPS

To add SSL/TLS support, update `nginx/nginx.conf`:

```nginx
server {
    listen 443 ssl http2;
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    # ... rest of configuration
}
```

And update `compose.yml` to map port 443 and mount certificates.

### Require Authentication for Reads

To require authentication for GET requests, modify `nginx/nginx.conf`:

```nginx
location / {
    # Remove the limit_except directive
    auth_basic "FPM Repository - Authentication Required";
    auth_basic_user_file /etc/nginx/.htpasswd;
    # ... rest of configuration
}
```

### Increase Upload Size Limit

Edit `nginx/nginx.conf`:

```nginx
client_max_body_size 1G;  # Change from 500M to 1G
```

Then restart:

```bash
podman-compose restart fpm-repo
```

## Backup and Restore

### Backup

```bash
# Backup using volume export
podman volume export fpm-repo-data > fpm-repo-backup-$(date +%Y%m%d).tar

# Or using podman cp (if container is running)
podman cp fpm-repository:/var/fpm-repo ./backup/
```

### Restore

```bash
# Stop the service
podman-compose down

# Import volume
podman volume import fpm-repo-data fpm-repo-backup-20241125.tar

# Start the service
podman-compose up -d
```

## Troubleshooting

### Permission Denied on Upload

**Problem**: Getting 403 Forbidden when trying to upload

**Solution**: 
- Verify credentials: `curl -u username:password http://localhost:8080/`
- Check `.htpasswd` file exists and is mounted correctly
- Review nginx logs: `podman-compose logs fpm-repo`

### Large File Upload Fails

**Problem**: Upload fails for large packages

**Solution**: Increase `client_max_body_size` in `nginx.conf` and restart

### Container Won't Start

**Problem**: Container fails to start

**Solution**:
```bash
# Check logs
podman-compose logs fpm-repo

# Verify nginx config syntax
podman run --rm -v ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro nginx:alpine nginx -t
```

### Health Check Failing

**Problem**: Health check reports unhealthy

**Solution**:
```bash
# Test directly
curl http://localhost:8080/health

# Check if nginx is running
podman exec fpm-repository ps aux | grep nginx
```

## Monitoring

### View Access Logs

```bash
# Real-time logs
podman-compose logs -f fpm-repo

# Access logs only
podman exec fpm-repository tail -f /var/log/nginx/access.log
```

### Check Disk Usage

```bash
# Volume size
podman volume inspect fpm-repo-data | grep -A 5 Mountpoint

# Inside container
podman exec fpm-repository du -sh /var/fpm-repo/*
```

## Maintenance

### Update Nginx

```bash
# Pull latest image
podman pull nginx:alpine

# Recreate container
podman-compose up -d --force-recreate
```

### Clean Up Old Packages

```bash
# Access container
podman exec -it fpm-repository sh

# Navigate and clean up
cd /var/fpm-repo
# Remove old versions manually or with scripts
```

## Integration with CI/CD

### GitLab CI Example

```yaml
publish-package:
  stage: deploy
  script:
    - fpm package --version $CI_COMMIT_TAG --org myorg
    - fpm publish myorg/my-app==$CI_COMMIT_TAG --repo production
  only:
    - tags
```

### GitHub Actions Example

```yaml
- name: Publish Package
  run: |
    fpm package --version ${{ github.ref_name }} --org myorg
    fpm publish myorg/my-app==${{ github.ref_name }} --repo production
```

## Support and Resources

- **FPM Documentation**: See main FPM repository README and VISION documents
- **Nginx WebDAV**: https://nginx.org/en/docs/http/ngx_http_dav_module.html
- **Podman Compose**: https://github.com/containers/podman-compose

## License

This deployment configuration follows the same license as the FPM project.

