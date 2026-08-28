#!/usr/bin/env bash
# ==============================================================================
# mirror-all.sh: Build and publish/republish catalog apps to FPM registry
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default values
PYTHON_VERSION="3.14"
REPO_NAME="default"
REPO_PASSWORD="${FPM_REPO_DEFAULT_PASSWORD:-}"
APPS=""
REPUBLISH=false
CLEAN=false
DRY_RUN=false
BUILD_CLI=true
PLATFORM="manylinux2014_x86_64"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m' # No Color

usage() {
    echo -e "${BOLD}Usage:${NC} $(basename "$0") [options]

${BOLD}Options:${NC}
  -p, --python-version <ver>   Python version for vendored wheels (default: ${PYTHON_VERSION})
  -a, --apps <slugs>           Comma-separated catalog slugs (e.g. erpnext,hrms; default: all enabled apps)
  -r, --republish, --force     Force rebuild and overwrite versions already present in the registry
      --repo <name>            Target repository name in FPM config (default: ${REPO_NAME})
      --clean                  Clear ~/.fpm/build-cache before starting
  -n, --dry-run                Print build plan without packaging or publishing
      --no-build               Skip compiling ./bin/fpm CLI from source
  -h, --help                   Show this help message

${BOLD}Examples:${NC}
  # Mirror ERPNext for Python 3.14 and overwrite existing package:
  $0 --apps erpnext --python-version 3.14 --republish

  # Mirror all enabled catalog apps for Python 3.14:
  $0 --python-version 3.14 --republish

  # Preview the build plan for all apps without publishing:
  $0 --dry-run"
    exit 0
}

# Parse command-line arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p|--python-version)
            PYTHON_VERSION="$2"
            shift 2
            ;;
        -a|--apps)
            APPS="$2"
            shift 2
            ;;
        -r|--republish|--force)
            REPUBLISH=true
            shift
            ;;
        --repo)
            REPO_NAME="$2"
            shift 2
            ;;
        --clean)
            CLEAN=true
            shift
            ;;
        -n|--dry-run)
            DRY_RUN=true
            shift
            ;;
        --no-build)
            BUILD_CLI=false
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo -e "${RED}Error: Unknown option $1${NC}" >&2
            usage
            ;;
    esac
done

cd "${ROOT_DIR}"

echo -e "${BLUE}======================================================${NC}"
echo -e "${BOLD} FPM Catalog Mirror Runner${NC}"
echo -e "${BLUE}======================================================${NC}"
echo -e "  ${BOLD}Target Repository:${NC}   ${REPO_NAME}"
echo -e "  ${BOLD}Python Version:${NC}      ${PYTHON_VERSION}"
echo -e "  ${BOLD}Platform:${NC}            ${PLATFORM}"
echo -e "  ${BOLD}Apps Filter:${NC}         ${APPS:-all enabled apps}"
echo -e "  ${BOLD}Force Republish:${NC}     ${REPUBLISH}"
echo -e "  ${BOLD}Clean Cache:${NC}         ${CLEAN}"
echo -e "  ${BOLD}Dry Run:${NC}             ${DRY_RUN}"
echo -e "${BLUE}------------------------------------------------------${NC}"

# Clean build cache if requested
if [ "${CLEAN}" = true ]; then
    CACHE_DIR="${HOME}/.fpm/build-cache"
    echo -e "${YELLOW}==> Cleaning build cache in ${CACHE_DIR}...${NC}"
    rm -rf "${CACHE_DIR}"
    echo -e "${GREEN}✓ Cache cleared.${NC}"
fi

# Build FPM CLI if needed
if [ "${BUILD_CLI}" = true ]; then
    echo -e "${YELLOW}==> Building fresh ./bin/fpm CLI...${NC}"
    make build
    echo -e "${GREEN}✓ CLI built successfully: ./bin/fpm${NC}"
elif [ ! -f "./bin/fpm" ]; then
    echo -e "${YELLOW}==> ./bin/fpm not found. Compiling CLI...${NC}"
    make build
    echo -e "${GREEN}✓ CLI built successfully: ./bin/fpm${NC}"
fi

# Export credentials for repository if provided
if [ -n "${REPO_PASSWORD}" ]; then
    export FPM_REPO_DEFAULT_PASSWORD="${REPO_PASSWORD}"
    if [ "${REPO_NAME}" != "default" ]; then
        REPO_ENV_VAR="FPM_REPO_$(echo "${REPO_NAME}" | tr '[:lower:]-' '[:upper:]_')_PASSWORD"
        export "${REPO_ENV_VAR}"="${REPO_PASSWORD}"
    fi
fi

# Construct mirror command arguments
MIRROR_ARGS=(
    "mirror"
    "--repo" "${REPO_NAME}"
    "--python-version" "${PYTHON_VERSION}"
    "--platform" "${PLATFORM}"
)

if [ -n "${APPS}" ]; then
    MIRROR_ARGS+=("--apps" "${APPS}")
fi

if [ "${REPUBLISH}" = true ]; then
    MIRROR_ARGS+=("--republish")
fi

if [ "${DRY_RUN}" = true ]; then
    MIRROR_ARGS+=("--dry-run")
fi

echo -e "\n${YELLOW}==> Executing: ./bin/fpm ${MIRROR_ARGS[*]}${NC}\n"

./bin/fpm "${MIRROR_ARGS[@]}"

echo -e "\n${GREEN}======================================================${NC}"
echo -e "${GREEN}✓ Mirror command completed successfully!${NC}"
echo -e "${GREEN}======================================================${NC}"
