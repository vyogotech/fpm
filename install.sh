#!/bin/bash
set -e

# Frappe Package Manager (FPM) Installer
# Detects OS/Arch and installs the latest release of fpm

ORG="vyogotech"
REPO="fpm"
BINARY_NAME="fpm"
INSTALL_DIR="/usr/local/bin"

echo "🔍 Detecting system information..."
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Map architecture names
case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "❌ Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "🚀 System: $OS ($ARCH)"

# Get latest release tag
echo "📡 Fetching latest release information..."
LATEST_TAG=$(curl -s "https://api.github.com/repos/$ORG/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_TAG" ]; then
    echo "❌ Could not fetch latest release. Please check your internet connection."
    exit 1
fi

echo "📦 Latest version: $LATEST_TAG"

# Construct download URL
# Binary format: fpm-linux-amd64
DOWNLOAD_URL="https://github.com/vyogotech/fpm/releases/download/$LATEST_TAG/fpm-$OS-$ARCH"

echo "📥 Downloading fpm..."
curl -LO "$DOWNLOAD_URL"

echo "⚙️  Installing fpm to $INSTALL_DIR..."
chmod +x "fpm-$OS-$ARCH"
sudo mv "fpm-$OS-$ARCH" "$INSTALL_DIR/$BINARY_NAME"

echo "✅ FPM has been successfully installed!"
echo "👉 Run 'fpm --help' to get started."
