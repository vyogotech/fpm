#!/usr/bin/env bash
# fpm package verification — installs published packages into a throwaway bench.
#
# Publishing proves an artifact exists. It does not prove the artifact works: the
# catalogue shipped front-end packages for months that installed and then rendered
# nothing, because nothing ever installed one (issue #9). This closes that gap by
# doing to a published package exactly what a user would:
#
#   fpm repo add  ->  fpm install <org>/<app>==<version> --site  ->  assert
#
# in a Single Node Frappista container that is thrown away afterwards. It asserts what
# a user would notice:
#
#   1. the install exits 0
#   2. the app is listed on the site
#   3. the app's DocTypes are in the database (an app that ships any)
#   4. the app's compiled bundles are in sites/assets/assets.json (a package that
#      claims assets_built), and each one is served over HTTP
#
# Usage:
#   test/verify/verify.sh crm,wiki           # named apps, latest published version
#   test/verify/verify.sh crm==1.82.0        # a specific version
#   test/verify/verify.sh all                # every app the registry index lists
#
# Environment:
#   FPM_VERIFY_REGISTRY  registry base URL   (default https://fpm.vyogo.tech)
#   FPM_VERIFY_IMAGE     bench image         (default docker.io/vyogo/erpnext:sne-develop,
#                                             which carries erpnext for apps that need it)
#   FPM_VERIFY_ORG       publishing org      (default frappe)
#   FPM_VERIFY_FPM       path to the fpm binary to drive (default: build from this tree)
#   FPM_VERIFY_KEEP=1    keep the container after a failure, to inspect it
#
# Run this on a host whose architecture matches what the packages were built for. The
# catalogue vendors wheels for manylinux2014_x86_64, so an arm64 host installs nothing:
# fpm refuses the mismatch rather than letting pip fail halfway through.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
REGISTRY="${FPM_VERIFY_REGISTRY:-https://fpm.vyogo.tech}"
IMAGE="${FPM_VERIFY_IMAGE:-docker.io/vyogo/erpnext:sne-develop}"
ORG="${FPM_VERIFY_ORG:-frappe}"
SITE="${FPM_VERIFY_SITE:-dev.localhost}"
BENCH=/home/frappe/frappe-bench
WORK="$HERE/.work"
OUT="$WORK/out"
USERNS=(--userns=keep-id:uid=1001,gid=0)
PATH_PREFIX="/opt/fpm:$BENCH/env/bin:/home/frappe/.local/bin"

log()  { printf '\n\033[1;34m[verify] %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32m  PASS\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m  FAIL\033[0m %s\n' "$*"; FAILED=1; }

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[ $# -ge 1 ] || usage

# One container per app: an install is not isolated from what a previous app did to the
# site, and a verification that depends on the order apps were tested in proves nothing.
verify_one() {
	local spec="$1" app version container
	app="${spec%%==*}"
	version="${spec#*==}"
	[ "$version" = "$app" ] && version=""
	container="fpm-verify-$app-$$"
	FAILED=0

	local target="$ORG/$app"
	[ -n "$version" ] && target="$target==$version"
	log "verifying $target in $IMAGE"

	podman rm -f "$container" >/dev/null 2>&1 || true
	podman run -d --name "$container" "${USERNS[@]}" \
		-v "$WORK/bin:/opt/fpm:ro,Z" \
		"$IMAGE" bash -c "sed -i '/^watch:/d' $BENCH/Procfile; exec /usr/libexec/s2i/run" >/dev/null

	local ready=0
	for _ in $(seq 1 90); do
		if cexec "$container" "(mariadb-admin ping || mysqladmin ping) >/dev/null 2>&1 && redis-cli ping >/dev/null 2>&1"; then ready=1; break; fi
		sleep 2
	done
	if [ "$ready" -ne 1 ]; then
		podman logs --tail 30 "$container" || true
		fail "$app: the bench never came up"
		cleanup "$container"
		record "$app" "$version" "bench-unavailable"
		return
	fi

	cexec "$container" "fpm repo add verify '$REGISTRY' --type http" >/dev/null 2>&1 || true

	# 1. the install itself
	if cexec "$container" "fpm install $target --bench-path $BENCH --site $SITE" > "$OUT/$app-install.log" 2>&1; then
		pass "$app installs onto $SITE"
	else
		fail "$app: install failed (see $OUT/$app-install.log)"
		tail -15 "$OUT/$app-install.log" | sed 's/^/      /'
		cleanup "$container"
		record "$app" "$version" "install-failed"
		return
	fi

	# The version actually installed, for the report: "latest" resolves at install time.
	local installed
	installed="$(cexec "$container" "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $SITE list-apps" 2>/dev/null | awk -v a="$app" '$1==a {print $2}' | head -1)"

	# 2. the site lists it
	if cexec "$container" "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $SITE list-apps" 2>/dev/null | grep -q "^$app "; then
		pass "$app is installed on $SITE (version $installed)"
	else
		fail "$app: not listed on the site after a successful install"
	fi

	# 3. its DocTypes reached the database — the state issue #13 left behind
	local expected synced
	expected="$(cexec "$container" "ls $BENCH/apps/$app/$app/*/doctype/*/*.json 2>/dev/null | wc -l" 2>/dev/null | tr -d ' ')"
	if [ "${expected:-0}" -gt 0 ]; then
		synced="$(cexec "$container" "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $SITE execute frappe.db.count --kwargs \"{'dt':'DocType','filters':{'app_name':'$app'}}\"" 2>/dev/null | grep -oE '^[0-9]+$' | tail -1)"
		# Older frappe has no app_name column on DocType; fall back to the app's modules.
		if [ -z "${synced:-}" ]; then
			synced="$(cexec "$container" "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $SITE execute frappe.db.count --kwargs \"{'dt':'Module Def','filters':{'app_name':'$app'}}\"" 2>/dev/null | grep -oE '^[0-9]+$' | tail -1)"
		fi
		if [ "${synced:-0}" -gt 0 ]; then
			pass "$app's DocTypes are in the database ($synced of $expected on disk)"
		else
			fail "$app: ships $expected DocType file(s) and the site has none — half-installed"
		fi
	else
		pass "$app ships no DocTypes; nothing to sync"
	fi

	# 4. compiled assets, when the package claims them
	local bundles
	bundles="$(cexec "$container" "cat /home/frappe/.fpm/apps/$ORG/$app/*/app_metadata.json 2>/dev/null" 2>/dev/null |
		python3 -c "import json,sys
try: d=json.load(sys.stdin)
except Exception: print(''); raise SystemExit
print(' '.join((d.get('asset_bundles') or {}).keys()))" 2>/dev/null || echo "")"
	if [ -n "$bundles" ]; then
		local missing=0 served=0
		for key in $bundles; do
			local path
			path="$(cexec "$container" "cat sites/assets/assets.json sites/assets/assets-rtl.json 2>/dev/null" 2>/dev/null |
				python3 -c "import json,sys,re
raw=sys.stdin.read()
for blob in re.findall(r'\{.*?\}', raw, re.S):
    try: d=json.loads(blob)
    except Exception: continue
    if '$key' in d: print(d['$key']); break" 2>/dev/null || true)"
			if [ -z "$path" ]; then missing=$((missing+1)); continue; fi
			if cexec "$container" "curl -sf -o /dev/null 'http://127.0.0.1:8000$path'" >/dev/null 2>&1; then
				served=$((served+1))
			else
				missing=$((missing+1))
			fi
		done
		if [ "$missing" -eq 0 ]; then
			pass "$app's $served compiled bundle(s) are in assets.json and served"
		else
			fail "$app: $missing of its compiled bundle(s) are missing from assets.json or not served"
		fi
	else
		pass "$app declares no compiled bundles"
	fi

	if [ "$FAILED" -eq 0 ]; then
		record "$app" "${installed:-$version}" "ok"
	else
		record "$app" "${installed:-$version}" "failed"
	fi
	cleanup "$container"
}

cexec() { podman exec -i -e HOME=/home/frappe "$1" bash -c "export PATH=$PATH_PREFIX:\$PATH; cd $BENCH; ${2}"; }

cleanup() {
	if [ "${FPM_VERIFY_KEEP:-}" = 1 ] && [ "${FAILED:-0}" -ne 0 ]; then
		log "keeping $1 for inspection (FPM_VERIFY_KEEP=1)"
		return
	fi
	podman rm -f "$1" >/dev/null 2>&1 || true
}

# A single-tab IFS still treats the tab as whitespace, so consecutive tabs collapse and
# an empty version would shift the result into its column. A placeholder keeps the three
# fields aligned however they are read back.
record() { printf '%s\t%s\t%s\n' "$1" "${2:--}" "$3" >> "$OUT/results.tsv"; }

# Every app the registry lists, for `all`.
list_registry_apps() {
	curl -sS --max-time 30 "$REGISTRY/metadata/index.json" |
		python3 -c "import json,sys
d=json.load(sys.stdin)
print(','.join(sorted({p['appName'] for p in d.get('packages', []) if p.get('org')=='$ORG'})))"
}

main() {
	command -v podman >/dev/null || { echo "podman is required" >&2; exit 1; }
	mkdir -p "$WORK/bin" "$OUT"
	: > "$OUT/results.tsv"

	if [ -n "${FPM_VERIFY_FPM:-}" ]; then
		# -ef: a caller that already staged the binary here (the workflow does) would
		# otherwise make cp refuse to copy a file onto itself and take the run with it.
		if [ ! "$FPM_VERIFY_FPM" -ef "$WORK/bin/fpm" ]; then
			cp "$FPM_VERIFY_FPM" "$WORK/bin/fpm"
		fi
	else
		log "building fpm for the image's architecture"
		local arch; arch="$(podman image inspect "$IMAGE" --format '{{.Architecture}}' 2>/dev/null || echo amd64)"
		(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$WORK/bin/fpm" ./cmd/fpm)
	fi
	chmod +x "$WORK/bin/fpm"
	podman image exists "$IMAGE" || { log "pulling $IMAGE"; podman pull -q "$IMAGE"; }

	local image_arch host_arch
	image_arch="$(podman image inspect "$IMAGE" --format '{{.Architecture}}' 2>/dev/null || echo unknown)"
	case "$(uname -m)" in arm64|aarch64) host_arch=arm64;; *) host_arch=amd64;; esac
	if [ "$image_arch" != "unknown" ] && [ "$image_arch" != "$host_arch" ]; then
		log "note: $IMAGE is $image_arch on a $host_arch host; it will run emulated and slowly"
	fi

	local specs="$1"
	[ "$specs" = "all" ] && specs="$(list_registry_apps)"
	log "verifying: $specs"

	local rc=0
	IFS=',' read -ra items <<< "$specs"
	for spec in "${items[@]}"; do
		[ -n "$spec" ] || continue
		verify_one "$spec" || rc=1
	done

	echo
	log "results"
	printf '%-18s %-24s %s\n' "APP" "VERSION" "RESULT"
	while IFS=$'\t' read -r app version result; do
		printf '%-18s %-24s %s\n' "$app" "${version:-?}" "$result"
		[ "$result" = "ok" ] || rc=1
	done < "$OUT/results.tsv"
	echo
	[ "$rc" -eq 0 ] && log "every package verified" || log "some packages did not verify (logs in $OUT)"
	return $rc
}

main "$@"
