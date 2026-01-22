# FPM VPS Deployment Guide (Non-Docker)

This guide explains how to host your own Frappe Package Manager (FPM) repository on a traditional VPS (like Milesweb) using native web servers (Apache or Nginx) instead of Docker.

---

## 1. Directory Structure
Create a dedicated folder for your repository. If you are using cPanel, this would typically be under `public_html/store`.

```bash
# Example for a VPS setup
sudo mkdir -p /var/www/fpm-repo/metadata
sudo chown -R www-data:www-data /var/www/fpm-repo
```

## 2. Authentication Setup
FPM requires authentication for publishing apps. We use a standard `.htpasswd` file.

```bash
# Install utils if missing
# Ubuntu/Debian: sudo apt install apache2-utils
# CentOS: sudo yum install httpd-tools

# Create the password file (Replace 'admin' with your username)
sudo htpasswd -c /etc/apache2/.fpm_htpasswd admin
```

## 3. Server Configuration

### Option A: Apache (Common on Milesweb/cPanel)
Enable the WebDAV module and configure the directory:

```apache
# 1. Enable modules
# sudo a2enmod dav dav_fs

# 2. Add to your VirtualHost or .htaccess
DocumentRoot /var/www/fpm-repo

<Directory /var/www/fpm-repo>
    Options Indexes FollowSymLinks
    Dav On
    
    # Allow public READ (GET)
    # Require password only for WRITE (PUT, MKCOL, DELETE)
    <LimitExcept GET OPTIONS>
        AuthType Basic
        AuthName "FPM App Store"
        AuthUserFile /etc/apache2/.fpm_htpasswd
        Require valid-user
    </LimitExcept>
</Directory>
```

### Option B: Nginx
```nginx
server {
    listen 80;
    server_name store.yourdomain.com;

    location / {
        root /var/fpm-repo;
        autoindex on;
        
        # Enable WebDAV
        dav_methods PUT DELETE MKCOL COPY MOVE;
        create_full_put_path on;
        dav_access user:rw group:r all:r;

        limit_except GET OPTIONS {
            auth_basic "FPM App Store";
            auth_basic_user_file /etc/nginx/.htpasswd;
        }
    }
}
```

---

## 4. Usage

### Local Client Setup
Add your new remote repository to your local FPM CLI:

```bash
fpm repo add milesweb http://store.yourdomain.com
```

### Publishing Apps
```bash
# Package your app
fpm package --version 1.0.0 --org myorg

# Publish to the VPS
fpm publish myorg/myapp==1.0.0 --repo milesweb
```

### Installing Apps
```bash
fpm install myorg/myapp==1.0.0 --site mysite.tech
```

> [!TIP]
> **Shared Hosting / cPanel shortcut**:
> If your Milesweb plan provides a **"Web Disk"** feature, you can skip all the manual configuration. Simply copy the Web Disk URL and credentials into FPM, as it is natively compatible with WebDAV.
