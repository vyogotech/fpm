#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

HTPASSWD_FILE="nginx/.htpasswd"

# Check if htpasswd is available
if ! command -v htpasswd &> /dev/null; then
    echo -e "${RED}Error: htpasswd command not found${NC}"
    echo "Please install apache2-utils (Debian/Ubuntu) or httpd-tools (RHEL/CentOS)"
    exit 1
fi

# Check if .htpasswd exists
if [ ! -f "$HTPASSWD_FILE" ]; then
    echo -e "${RED}Error: $HTPASSWD_FILE not found${NC}"
    echo "Please run ./scripts/setup.sh first"
    exit 1
fi

# Get username from argument or prompt
if [ -n "$1" ]; then
    USERNAME="$1"
else
    echo -n "Enter username: "
    read USERNAME
fi

if [ -z "$USERNAME" ]; then
    echo -e "${RED}Error: Username cannot be empty${NC}"
    exit 1
fi

# Check if user already exists
if grep -q "^${USERNAME}:" "$HTPASSWD_FILE" 2>/dev/null; then
    echo -e "${RED}Error: User '$USERNAME' already exists${NC}"
    read -p "Do you want to update the password? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
    # Remove existing user
    sed -i.bak "/^${USERNAME}:/d" "$HTPASSWD_FILE"
    rm -f "${HTPASSWD_FILE}.bak"
fi

# Add user
htpasswd -b "$HTPASSWD_FILE" "$USERNAME" "$(read -sp "Enter password: " pwd; echo $pwd)"
echo
echo -e "${GREEN}✓${NC} User '$USERNAME' added successfully"

# Check if using podman or docker
if command -v podman &> /dev/null; then
    COMPOSE_CMD="podman-compose"
elif command -v docker &> /dev/null; then
    COMPOSE_CMD="docker compose"
else
    echo ""
    echo "To apply changes, restart the repository:"
    echo "  $COMPOSE_CMD restart fpm-repo"
    exit 0
fi

# Check if container is running
if $COMPOSE_CMD ps | grep -q "fpm-repo"; then
    echo ""
    read -p "Repository is running. Restart to apply changes? (y/N): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        $COMPOSE_CMD restart fpm-repo
        echo -e "${GREEN}✓${NC} Repository restarted"
    fi
fi


