#!/bin/bash
set -e

echo "=== FPM Repository Setup ==="
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if htpasswd is available
if ! command -v htpasswd &> /dev/null; then
    echo -e "${RED}Error: htpasswd command not found${NC}"
    echo "Please install apache2-utils (Debian/Ubuntu) or httpd-tools (RHEL/CentOS)"
    echo "  - Ubuntu/Debian: sudo apt-get install apache2-utils"
    echo "  - RHEL/CentOS: sudo dnf install httpd-tools"
    echo "  - macOS: brew install httpd"
    exit 1
fi

# Check if podman or docker is available
if command -v podman &> /dev/null; then
    CONTAINER_CMD="podman"
    COMPOSE_CMD="podman-compose"
    echo -e "${GREEN}✓${NC} Found Podman"
elif command -v docker &> /dev/null; then
    CONTAINER_CMD="docker"
    COMPOSE_CMD="docker compose"
    echo -e "${GREEN}✓${NC} Found Docker"
else
    echo -e "${RED}Error: Neither podman nor docker found${NC}"
    exit 1
fi

# Create nginx directory if it doesn't exist
mkdir -p nginx

# Create .htpasswd file
HTPASSWD_FILE="nginx/.htpasswd"

if [ -f "$HTPASSWD_FILE" ]; then
    echo -e "${YELLOW}Warning: .htpasswd file already exists${NC}"
    read -p "Do you want to overwrite it? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Keeping existing .htpasswd file"
    else
        rm "$HTPASSWD_FILE"
        echo -e "${GREEN}✓${NC} Removed old .htpasswd file"
    fi
fi

if [ ! -f "$HTPASSWD_FILE" ]; then
    echo ""
    echo "Creating admin user for FPM repository..."
    echo -n "Enter username [admin]: "
    read USERNAME
    USERNAME=${USERNAME:-admin}
    
    htpasswd -c "$HTPASSWD_FILE" "$USERNAME"
    echo -e "${GREEN}✓${NC} Created .htpasswd with user: $USERNAME"
fi

# Create .env file if it doesn't exist
if [ ! -f ".env" ]; then
    if [ -f ".env.example" ]; then
        cp .env.example .env
        echo -e "${GREEN}✓${NC} Created .env file from .env.example"
    else
        echo "FPM_REPO_PORT=8080" > .env
        echo -e "${GREEN}✓${NC} Created default .env file"
    fi
fi

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Next steps:"
echo "  1. Review and edit .env file if needed"
echo "  2. Start the repository: $COMPOSE_CMD up -d"
echo "  3. Check logs: $COMPOSE_CMD logs -f"
echo "  4. Test health: curl http://localhost:8080/health"
echo ""
echo "To add more users, run: ./scripts/add-user.sh <username>"
echo ""


