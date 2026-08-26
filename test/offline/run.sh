#!/usr/bin/env bash
# fpm offline integration test — local driver.
#
# Runs the scenario from GOAL item 8 against a real, network-isolated bench on a
# remote podman host (over SSH), using a Single Node Frappista image
# (docker.io/vyogo/frappe:sne-<branch>: MariaDB, Redis, bench, a ready site):
#
#   1. cross-build fpm for the host and rsync it with the fixture apps;
#   2. on the host, pull the frappista image (online);
#   3. package the fixture apps inside that image with --bundle-deps and
#      --bench-path (online: pip download from PyPI, Frappe's own asset build);
#   4. start the image with --network none, prove it has no outbound network,
#      and `fpm install` every package onto its site;
#   5. assert: nothing was fetched, Python deps came from vendored wheels only,
#      required_apps were satisfied from the local store, assets.json is
#      equivalent to a live `bench build` of the same apps, and the site serves
#      the apps' JS/CSS over HTTP.
#
# Usage:
#   test/offline/run.sh all            # everything, in order
#   test/offline/run.sh sync           # (re)build fpm and rsync to the host
#   test/offline/run.sh image          # pull the frappista image on the host
#   test/offline/run.sh package        # package fixtures on the host (online)
#   test/offline/run.sh offline        # network-isolated install + assertions
#   test/offline/run.sh verify         # re-run assertions against the running container
#   test/offline/run.sh ui             # headless-browser screenshots of the isolated site
#   test/offline/run.sh tunnel         # expose the site on the host's LAN (UNIX-socket relay)
#   test/offline/run.sh real           # real erpnext + hrms: package --with-deps, install bundle offline
#   test/offline/run.sh clean          # remove the container on the host
#
# Environment:
#   FPM_OFFLINE_SSH_HOST     user@host            (default varun@192.168.1.111)
#   FPM_OFFLINE_REMOTE_DIR   work dir on the host (default fpm-offline-test)
#   FPM_OFFLINE_IMAGE        bench image          (default docker.io/vyogo/frappe:sne-develop)
#   FPM_OFFLINE_GOARCH       host architecture    (default amd64)
#   FPM_OFFLINE_LOCAL=1      run on this machine's podman instead of over SSH
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FPM_ROOT="$(cd "$HERE/../.." && pwd)"
HOST="${FPM_OFFLINE_SSH_HOST:-varun@192.168.1.111}"
REMOTE_DIR="${FPM_OFFLINE_REMOTE_DIR:-fpm-offline-test}"
GOARCH="${FPM_OFFLINE_GOARCH:-amd64}"
WORK="$HERE/.work"
# FPM_OFFLINE_LOCAL=1 runs everything on this machine's podman instead of over SSH:
# the work dir is .work/local, fpm is built for this machine's architecture, and the
# frappista images' matching variant (amd64/arm64) is used.
LOCAL="${FPM_OFFLINE_LOCAL:-0}"
if [ "$LOCAL" = 1 ]; then
	WORK="$HERE/.work/local"
	case "$(uname -m)" in arm64|aarch64) GOARCH="${FPM_OFFLINE_GOARCH:-arm64}";; *) GOARCH="${FPM_OFFLINE_GOARCH:-amd64}";; esac
fi
# Forwarded to remote.sh.
REMOTE_ENV="FPM_OFFLINE_IMAGE='${FPM_OFFLINE_IMAGE:-}' FPM_OFFLINE_PYTHON='${FPM_OFFLINE_PYTHON:-}' FPM_OFFLINE_REAL_REUSE='${FPM_OFFLINE_REAL_REUSE:-}' FPM_OFFLINE_REAL_KEEP='${FPM_OFFLINE_REAL_KEEP:-}' FPM_OFFLINE_ERPNEXT_REF='${FPM_OFFLINE_ERPNEXT_REF:-}' FPM_OFFLINE_HRMS_REF='${FPM_OFFLINE_HRMS_REF:-}'"

log() { printf '\n\033[1;34m[run.sh] %s\033[0m\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "missing tool: $1" >&2; exit 1; }; }

phase_sync() {
	need go; need rsync; need ssh
	log "Cross-building fpm for linux/$GOARCH"
	mkdir -p "$WORK/bin"
	(cd "$FPM_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
		-ldflags "-X 'fpm/cmd.version=offline-test' -X 'fpm/cmd.commit=$(git -C "$FPM_ROOT" rev-parse --short HEAD 2>/dev/null || echo dev)'" \
		-o "$WORK/bin/fpm" ./cmd/fpm)

	if [ "$LOCAL" = 1 ]; then
		log "Staging into $WORK (local mode)"
		mkdir -p "$WORK/apps" "$WORK/ui"
		cp "$HERE/remote.sh" "$WORK/remote.sh"
		rsync -a --delete "$HERE/ui/" "$WORK/ui/"
		rsync -a --delete --exclude '__pycache__' --exclude 'public/dist' --exclude '.git' "$HERE/apps/" "$WORK/apps/"
		echo "fpm: $(git -C "$FPM_ROOT" rev-parse HEAD 2>/dev/null || echo dev) ($(git -C "$FPM_ROOT" branch --show-current 2>/dev/null))" > "$WORK/SOURCES.txt"
		return
	fi
	log "Syncing to $HOST:$REMOTE_DIR"
	ssh "$HOST" "mkdir -p '$REMOTE_DIR/bin'"
	rsync -az --delete "$WORK/bin/" "$HOST:$REMOTE_DIR/bin/"
	rsync -az "$HERE/remote.sh" "$HOST:$REMOTE_DIR/"
	rsync -az --delete "$HERE/ui/" "$HOST:$REMOTE_DIR/ui/"
	rsync -az --delete --exclude '__pycache__' --exclude 'public/dist' --exclude '.git' "$HERE/apps/" "$HOST:$REMOTE_DIR/apps/"
	echo "fpm: $(git -C "$FPM_ROOT" rev-parse HEAD 2>/dev/null || echo dev) ($(git -C "$FPM_ROOT" branch --show-current 2>/dev/null))" > "$WORK/SOURCES.txt"
	rsync -az "$WORK/SOURCES.txt" "$HOST:$REMOTE_DIR/"
}

remote() {
	local phase="$1"
	if [ "$LOCAL" = 1 ]; then
		log "Local phase: $phase"
		# FPM_OFFLINE_* are inherited from this shell's environment.
		(cd "$WORK" && bash remote.sh "$phase")
		return
	fi
	log "Remote phase: $phase"
	ssh -t "$HOST" "cd '$REMOTE_DIR' && env $REMOTE_ENV bash remote.sh '$phase'"
}

phase_fetch() {
	if [ "$LOCAL" = 1 ]; then
		echo "Results in $WORK/out (see RESULT.md)"
		return
	fi
	log "Fetching results to $WORK/results"
	mkdir -p "$WORK/results"
	rsync -az "$HOST:$REMOTE_DIR/out/" "$WORK/results/"
	echo "Results in $WORK/results (see RESULT.md)"
}

case "${1:-all}" in
	sync)    phase_sync ;;
	image)   remote image ;;
	package) remote package ;;
	offline) remote offline; phase_fetch ;;
	verify)  remote verify; phase_fetch ;;
	ui)      remote ui; phase_fetch ;;
	tunnel)  remote tunnel ;;
	real)    remote real; phase_fetch ;;
	fetch)   phase_fetch ;;
	clean)   remote clean ;;
	all)     phase_sync; remote image; remote package; remote offline; phase_fetch ;;
	*) echo "unknown phase: $1" >&2; exit 2 ;;
esac
