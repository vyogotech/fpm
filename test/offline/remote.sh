#!/usr/bin/env bash
# fpm offline integration test — host side (run by run.sh over SSH).
#
# The bench is a Single Node Frappista image (docker.io/vyogo/frappe:sne-<branch>):
# MariaDB, Redis and a bench at /home/frappe/frappe-bench with a ready site,
# all in one container, started by /usr/libexec/s2i/run. The same image plays
# both roles: the online builder for `fpm package`, and the network-isolated
# target for `fpm install`.
#
# Phases:
#   image    pull the image
#   package  package the fixture apps inside the image, online (pip + bench build)
#   offline  run the image with --network none, prove isolation, fpm install
#            everything onto its site, then run every assertion (also `verify`)
#   clean    remove the container (CLEAN_ALL=1 also removes out/)
set -euo pipefail

WORK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${FPM_OFFLINE_IMAGE:-docker.io/vyogo/frappe:sne-develop}"
NAME="${FPM_OFFLINE_CONTAINER:-fpm-offline}"
SITE="${FPM_OFFLINE_SITE:-dev.localhost}"
ADMIN_PASSWORD="${FPM_OFFLINE_ADMIN_PASSWORD:-admin}"
ORG=fpmtest
VERSION=1.0.0
PYTHON_VERSION="${FPM_OFFLINE_PYTHON:-}"
# Every manylinux tag an AlmaLinux 9 (glibc 2.34) bench accepts, for the image's
# architecture (set once the image is known; see image_arch). pip only expands the
# legacy manylinux2014 alias, so the PEP 600 spellings are listed too.
PLATFORMS=()
platforms_for_image() {
	local arch
	arch="$(podman image inspect "$IMAGE" --format '{{.Architecture}}' 2>/dev/null || echo amd64)"
	local wheel_arch=x86_64
	[ "$arch" = arm64 ] && wheel_arch=aarch64
	PLATFORMS=("manylinux2014_$wheel_arch" "manylinux_2_17_$wheel_arch" "manylinux_2_28_$wheel_arch" "manylinux_2_34_$wheel_arch")
}
BENCH=/home/frappe/frappe-bench
OUT="$WORK/out"
APPS=(fpm_demo_base fpm_demo_child fpm_demo_plain)
# The image runs as uid 1001 (frappe, gid 0); map this host user onto it so bind
# mounts are writable from inside and image files stay owned by their user.
USERNS=(--userns=keep-id:uid=1001,gid=0)
# Prepended to the image's own PATH (which is where frappista keeps node, yarn and bench).
PATH_PREFIX="/opt/fpm:$BENCH/env/bin:/home/frappe/.local/bin"

log()  { printf '\n\033[1;32m[remote.sh] %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; echo "FAIL: $*" >> "$OUT/RESULT.md" 2>/dev/null || true; exit 1; }
pass() { printf '\033[1;32mPASS: %s\033[0m\n' "$*"; echo "- PASS: $*" >> "$OUT/RESULT.md"; }

platform_flags() { [ "${#PLATFORMS[@]}" -gt 0 ] || platforms_for_image; for p in "${PLATFORMS[@]}"; do printf -- '--platform %s ' "$p"; done; }
cexec() { podman exec -i -e HOME=/home/frappe "$NAME" bash -c "export PATH=$PATH_PREFIX:\$PATH; cd $BENCH; $*"; }
# A frappe CLI command run the way `bench --site <site> …` runs it (bench/cli.py frappe_cmd),
# so the test does not depend on where the image keeps the bench wrapper.
frappe_cmd() { cexec "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $SITE $*"; }


# relabel_shared makes files that appeared in the shared directories after the
# container started readable from inside it. A :Z mount is labelled for the container
# at mount time; a binary re-synced later, or packages written by another container
# (the online builder, with its own label), get labels the running container cannot
# read under SELinux — the symptom is "fpm: command not found".
relabel_shared() {
	command -v chcon >/dev/null 2>&1 || return 0
	chcon -R -t container_file_t -l s0 "$WORK/bin" "$OUT" "$WORK/ui" 2>/dev/null || true
}

image_python_version() {
	podman run --rm "$IMAGE" bash -c "$BENCH/env/bin/python -c 'import sys; print(\"%d.%d\" % sys.version_info[:2])'"
}

phase_image() {
	log "Pulling $IMAGE"
	podman pull -q "$IMAGE"
	podman image inspect "$IMAGE" --format 'arch={{.Architecture}} created={{.Created}} size={{.Size}}'
	log "Bench interpreter: python $(image_python_version)"
	podman run --rm "$IMAGE" bash -c "cd $BENCH && node --version && ls env/bin | tr '\n' ' ' && echo && cat sites/apps.txt && head -c 300 sites/assets/assets.json && echo && cat Procfile"
}

phase_package() {
	[ -n "$PYTHON_VERSION" ] || PYTHON_VERSION="$(image_python_version)"
	platforms_for_image
	log "Packaging fixture apps inside $IMAGE (online), wheels for python $PYTHON_VERSION / ${PLATFORMS[*]}"
	rm -rf "$OUT" "$WORK/builder-home"
	mkdir -p "$OUT" "$WORK/builder-home/.fpm"
	# Fresh source tree per run (the asset build writes public/dist into it).
	find "$WORK/apps" -type d \( -name dist -o -name .git \) -prune -exec rm -rf {} +
	chmod -R u+w "$WORK/apps"

	local flags
	flags="--org $ORG --version $VERSION --output-path /out --bench-path $BENCH --python-version $PYTHON_VERSION $(platform_flags)"
	podman run --rm "${USERNS[@]}" \
		-v "$WORK/apps:/work/apps:Z" -v "$OUT:/out:Z" -v "$WORK/bin:/opt/fpm:ro,Z" \
		-v "$WORK/builder-home/.fpm:/home/frappe/.fpm:Z" \
		-e HOME=/home/frappe \
		"$IMAGE" bash -c '
set -euo pipefail
export PATH='"$PATH_PREFIX"':$PATH
cd /work/apps
git config --global user.email fpm@test && git config --global user.name fpm-test
for app in '"${APPS[*]}"'; do
  # Each fixture becomes a git checkout so the package records a real commit SHA.
  (cd $app && git init -q -b main && git remote add origin https://github.com/'"$ORG"'/$app.git && git add -A && git commit -qm "fixture $app")
  git -C $app rev-parse HEAD > /out/$app.commit
done

echo "=== 1. a non-Frappe checkout is rejected before any work (expect exit 3)"
set +e; fpm package not_a_frappe_app --org '"$ORG"' --version '"$VERSION"' --output-path /out --skip-local-install > /out/package-notapp.log 2>&1; rc=$?; set -e
cat /out/package-notapp.log
[ "$rc" -eq 3 ] || { echo "expected exit code 3 for not_a_frappe_app, got $rc"; exit 1; }
echo "exit=$rc" >> /out/package-notapp.log

echo "=== 2. a required_apps entry that is not published fails packaging (expect exit 5)"
set +e; fpm package fpm_demo_child '"$flags"' --skip-local-install > /out/package-child-unresolved.log 2>&1; rc=$?; set -e
tail -5 /out/package-child-unresolved.log
[ "$rc" -eq 5 ] || { echo "expected exit code 5 for unresolved required app, got $rc"; exit 1; }
echo "exit=$rc" >> /out/package-child-unresolved.log

echo "=== 3. package fpm_demo_base (third-party deps + built assets); lands in the builder local store"
fpm package fpm_demo_base '"$flags"' 2>&1 | tee /out/package-base.log
echo "=== 4. package fpm_demo_child (required_apps -> pinned against the local store)"
fpm package fpm_demo_child '"$flags"' 2>&1 | tee /out/package-child.log
echo "=== 5. package fpm_demo_plain (no JS, no Python deps)"
fpm package fpm_demo_plain '"$flags"' 2>&1 | tee /out/package-plain.log

echo "=== 6. dependency closure bundle: child + everything it requires, each once"
fpm bundle '"$ORG"'/fpm_demo_child=='"$VERSION"' --output /out/bundle-child 2>&1 | tee /out/bundle-child.log
cat /out/bundle-child/fpm-bundle.json

echo "=== 7. metadata queries external tooling would make"
fpm deps /out/fpm_demo_child-'"$VERSION"'.fpm --json > /out/deps-child.json
fpm deps /out/fpm_demo_base-'"$VERSION"'.fpm > /out/deps-base.txt
fpm exists '"$ORG"'/fpm_demo_base=='"$VERSION"' --commit "$(cat /out/fpm_demo_base.commit)" --platform '"${PLATFORMS[0]}"' --python-version '"$PYTHON_VERSION"' --json > /out/exists-base.json
set +e; fpm exists '"$ORG"'/fpm_demo_base=='"$VERSION"' --commit 0000000000000000000000000000000000000000 --json > /out/exists-base-wrongcommit.json; echo "exit=$?" >> /out/exists-base-wrongcommit.json; set -e
for f in /out/*.fpm; do python3 -m zipfile -l "$f" > "/out/$(basename "$f").list"; done
python3 - <<EOF
import zipfile
for app in "'"${APPS[*]}"'".split():
    z = zipfile.ZipFile(f"/out/{app}-'"$VERSION"'.fpm")
    open(f"/out/{app}.metadata.json", "wb").write(z.read("app_metadata.json"))
    if "wheels/fpm-lock.txt" in z.namelist():
        open(f"/out/{app}.lock", "wb").write(z.read("wheels/fpm-lock.txt"))
EOF
ls -la /out
'
	log "Packages built:"; ls -la "$OUT"/*.fpm
}

container_up() {
	# Anything joined to the old container's network namespace (the tunnel relay) must
	# go first, or podman refuses to remove the container.
	podman rm -f "$NAME-relay" >/dev/null 2>&1 || true
	pkill -f "relay.py tcp2unix" >/dev/null 2>&1 || true
	podman rm -f "$NAME" >/dev/null 2>&1 || true
	# The image's own entrypoint brings up MariaDB and Redis and then runs
	# `bench start` (honcho: web, socketio, schedule, worker — and an esbuild watcher
	# that would rebuild every app in apps/ in development mode and rewrite
	# assets.json). Those long-running Python processes cannot see an app installed
	# into the bench after they started — a real bench needs `bench restart` after
	# installing apps, and honcho tears everything down when the scheduler trips over
	# that — so `bench start` is replaced by a sleep for the install phase, and the web
	# server is started afresh afterwards for the HTTP checks. Everything else in the
	# entrypoint runs unchanged.
	mkdir -p "$WORK/stub"
	cat > "$WORK/stub/bench" <<'EOS'
#!/bin/bash
# Test stub: `bench start` -> keep the container alive; anything else -> the real bench.
if [ "${1:-}" = "start" ]; then exec sleep infinity; fi
for real in /home/frappe/.local/bin/bench /var/lib/redis/.local/bin/bench; do
  [ -x "$real" ] && exec "$real" "$@"
done
echo "real bench CLI not found" >&2; exit 127
EOS
	chmod +x "$WORK/stub/bench"
	log "Starting $IMAGE as $NAME with --network none"
	podman run -d --name "$NAME" --network none "${USERNS[@]}" \
		-v "$OUT:/out:ro,Z" -v "$WORK/bin:/opt/fpm:ro,Z" -v "$WORK/stub:/opt/fpm-stub:ro,Z" \
		"$IMAGE" bash -c "export PATH=/opt/fpm-stub:\$PATH; sed -i '/^watch:/d' $BENCH/Procfile; exec /usr/libexec/s2i/run" >/dev/null
	log "Waiting for MariaDB and Redis inside the container"
	for i in $(seq 1 90); do
		if cexec "(mariadb-admin ping || mysqladmin ping) >/dev/null 2>&1 && redis-cli ping >/dev/null 2>&1"; then break; fi
		sleep 2
		if [ "$i" -eq 90 ]; then podman logs --tail 50 "$NAME"; fail "services did not come up"; fi
	done
	sleep 3
	podman logs "$NAME" > "$OUT/container-startup.log" 2>&1 || true
}

# start_web brings up the site's web server (what the `web:` Procfile entry runs) after
# the apps are installed, so it starts with them importable.
start_web() {
	if cexec "curl -s -m 3 http://127.0.0.1:8000/api/method/ping" 2>/dev/null | grep -q pong; then return; fi
	log "Starting the web server (frappe serve) inside the isolated container"
	cexec "cd sites && nohup ../env/bin/python -m frappe.utils.bench_helper frappe serve --port 8000 > /tmp/serve.log 2>&1 &"
	for i in $(seq 1 60); do
		if cexec "curl -s -m 3 http://127.0.0.1:8000/api/method/ping" 2>/dev/null | grep -q pong; then return; fi
		sleep 2
	done
	cexec "cat /tmp/serve.log" || true
	fail "web server did not answer /api/method/ping"
}

prove_isolation() {
	log "Proving the container has no outbound network"
	local report="$OUT/network-isolation.log"
	{
		echo "container network mode: $(podman inspect "$NAME" --format '{{.HostConfig.NetworkMode}}')"
		echo "--- interfaces inside the container:"
		cexec 'ip -o addr show 2>/dev/null || cat /proc/net/dev'
		echo "--- DNS:"
		cexec 'getent hosts pypi.org || echo "DNS resolution failed (expected)"'
		echo "--- TCP to 1.1.1.1:443 and pypi.org:443 with python:"
		cexec 'python3 - <<EOF
import socket
for host in ("1.1.1.1", "pypi.org", "files.pythonhosted.org", "registry.npmjs.org"):
    try:
        socket.create_connection((host, 443), timeout=3).close()
        print("CONNECTED to", host, "(unexpected)")
    except Exception as e:
        print("no route to", host, "->", type(e).__name__, e)
EOF'
		echo "--- curl https://pypi.org/simple/ :"
		cexec 'curl -sS -m 5 https://pypi.org/simple/ -o /dev/null && echo "curl SUCCEEDED (unexpected)" || echo "curl failed (expected)"'
	} 2>&1 | tee "$report"
	grep -q 'network mode: none' "$report" || fail "container is not running with --network none"
	if grep -q 'CONNECTED to\|curl SUCCEEDED' "$report"; then fail "the container can reach the network; isolation is not in effect"; fi
	if cexec 'ip -o -4 addr show 2>/dev/null' | grep -v ' lo ' | grep -q 'inet '; then fail "container has a non-loopback address"; fi
	pass "container is network-isolated (NetworkMode=none; only loopback; DNS and TCP to PyPI/npm fail)"
}

phase_offline() {
	[ -d "$OUT" ] || fail "no packages in $OUT; run the package phase first"
	: > "$OUT/RESULT.md"
	{
		echo "# fpm offline integration test — $(date -u +%FT%TZ) on $(hostname)"
		echo; echo "image: $IMAGE"; cat "$WORK/SOURCES.txt" 2>/dev/null || true; echo
	} >> "$OUT/RESULT.md"

	container_up
	prove_isolation

	log "Installing packages offline, in dependency order"
	# The child first, on purpose: its required app is not in this bench's local store
	# yet, so the pre-install check must refuse (exit 6) without touching anything.
	set +e
	cexec "fpm install /out/fpm_demo_child-$VERSION.fpm --bench-path $BENCH --site $SITE" > "$OUT/install-child-refused.log" 2>&1
	rc=$?
	set -e
	echo "exit=$rc" >> "$OUT/install-child-refused.log"
	tail -6 "$OUT/install-child-refused.log"
	[ "$rc" -eq 6 ] || fail "installing the child before its required app should exit 6, got $rc"
	cexec "test ! -e apps/fpm_demo_child" || fail "refused install must not link the app into the bench"
	pass "required app missing from local store -> install refused (exit 6), bench untouched"

	for app in "${APPS[@]}"; do
		log "fpm install $app"
		cexec "fpm install /out/$app-$VERSION.fpm --bench-path $BENCH --site $SITE" 2>&1 | tee "$OUT/install-$app.log"
	done

	bundle_demo
	phase_verify
}

# bundle_demo installs the exported closure bundle (child + base) as one unit onto a
# second, fresh site: what a sidecar would ship to an offline bench.
BUNDLE_SITE="${FPM_OFFLINE_BUNDLE_SITE:-bundle.localhost}"
bundle_demo() {
	[ -d "$OUT/bundle-child" ] || { echo "no bundle exported; skipping bundle demo"; return; }
	log "Bundle demo: new site $BUNDLE_SITE, then 'fpm install /out/bundle-child' (base then child, offline)"
	new_site "$BUNDLE_SITE"
	cexec "fpm install /out/bundle-child --bench-path $BENCH --site $BUNDLE_SITE" 2>&1 | tee "$OUT/install-bundle.log"
	grep -q '\[1/2\] '"$ORG"'/fpm_demo_base=='"$VERSION" "$OUT/install-bundle.log" || fail "bundle did not install base first"
	grep -q '\[2/2\] '"$ORG"'/fpm_demo_child=='"$VERSION" "$OUT/install-bundle.log" || fail "bundle did not install child second"
	cexec "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $BUNDLE_SITE list-apps" > "$OUT/list-apps-bundle.txt"
	grep -q fpm_demo_base "$OUT/list-apps-bundle.txt" && grep -q fpm_demo_child "$OUT/list-apps-bundle.txt" \
		|| fail "bundle apps not installed on $BUNDLE_SITE"
	pass "closure bundle (fpm_demo_child + required fpm_demo_base) installed as one unit onto $BUNDLE_SITE, dependency first, offline"
}

# new_site creates a site inside the isolated container (only the local MariaDB is involved).
new_site() {
	local site="$1"
	cexec "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe new-site $site --db-root-username root --db-root-password \$MYSQL_ROOT_PASSWORD --admin-password $ADMIN_PASSWORD --mariadb-user-host-login-scope='%' --force" \
		> "$OUT/new-site-$site.log" 2>&1 || { tail -20 "$OUT/new-site-$site.log"; fail "new-site $site failed"; }
}

phase_verify() {
	relabel_shared
	log "Verifying"
	# --- 1. nothing was fetched: pip ran --no-index against vendored wheels only ---
	for app in "${APPS[@]}"; do
		grep -q -- '--no-index --find-links' "$OUT/install-$app.log" || fail "$app: pip was not run with --no-index --find-links"
		if grep -Ei 'Downloading https?://|Collecting .* from https?://' "$OUT/install-$app.log"; then fail "$app: pip downloaded something"; fi
	done
	pass "every fpm install ran pip with --no-index --find-links <vendored wheels>; no download lines"

	# --- 1b. nothing was built at install time: no node/yarn/esbuild, no wheel builds ---
	for app in "${APPS[@]}"; do
		if grep -Ei 'esbuild|yarn|bench build|node esbuild|Building wheel for|Running setup\.py|Preparing metadata \(setup\.py\)' "$OUT/install-$app.log"; then
			fail "$app: install performed a build"
		fi
	done
	pass "no build at install: no esbuild/yarn/node and no wheel builds in any install log (only the app's own editable metadata via vendored flit_core)"

	# --- 2. Python deps installed only from vendored wheels, at the locked versions ---
	cexec "env/bin/pip show tabulate msgpack" > "$OUT/pip-show.txt" 2>&1 || fail "vendored deps are not installed in the bench env"
	cexec "env/bin/pip freeze --all" | tr 'A-Z_' 'a-z-' > "$OUT/pip-freeze.txt"
	while IFS= read -r pin; do
		[ -n "$pin" ] || continue
		case "$pin" in \#*) continue;; esac
		name="${pin%%==*}"; ver="${pin##*==}"
		case "$name" in flit-core) continue;; esac  # build backend: used in isolation, never installed
		grep -qx "$name==$ver" "$OUT/pip-freeze.txt" || fail "locked pin $pin is not what the bench env has"
	done < "$OUT/fpm_demo_base.lock"
	frappe_cmd "execute fpm_demo_base.utils.demo_table" > "$OUT/demo_table.txt" 2>&1 \
		|| fail "the app cannot import its vendored dependencies on the site"
	pass "Python deps match wheels/fpm-lock.txt pins and import on the site ($(grep -v '^#' "$OUT/fpm_demo_base.lock" | tr '\n' ' '))"

	# --- 3. required_apps satisfied from already-installed local packages ---
	grep -q "Required app $ORG/fpm_demo_base==$VERSION satisfied from local FPM store" "$OUT/install-fpm_demo_child.log" \
		|| fail "child install did not report its required app satisfied from the local store"
	frappe_cmd "list-apps" > "$OUT/list-apps.txt"
	for app in "${APPS[@]}"; do grep -q "$app" "$OUT/list-apps.txt" || fail "$app not installed on site"; done
	pass "required_apps satisfied from the local FPM store; all apps installed on $SITE"

	# --- 4. assets.json equivalent to a live bench build of the same apps ---
	cexec "cat sites/assets/assets.json" > "$OUT/offline-assets.json"
	cexec "cat sites/assets/assets-rtl.json" > "$OUT/offline-assets-rtl.json"
	reference_build
	python3 - "$OUT" <<'EOF'
import json, re, sys, pathlib
out = pathlib.Path(sys.argv[1])
def load(name):
    raw = (out / name).read_text()
    if raw and raw != "{}":
        # JSON.stringify(obj, null, 4): 4-space indent, no trailing newline.
        assert raw.startswith("{\n    \"") and raw.endswith("\n}"), f"{name}: not JSON.stringify(_, null, 4) shaped"
        for line in raw.splitlines()[1:-1]:
            assert re.fullmatch(r'    "[^"]+": "[^"]+",?', line), f"{name}: unexpected line {line!r}"
    return json.loads(raw or "{}")
problems = []
for pair in (("offline-assets.json", "reference-assets.json"), ("offline-assets-rtl.json", "reference-assets-rtl.json")):
    a, b = load(pair[0]), load(pair[1])
    if a != b:
        for k in sorted(set(a) | set(b)):
            if a.get(k) != b.get(k):
                problems.append(f"{pair[0]}: {k}: fpm={a.get(k)} bench={b.get(k)}")
    print(f"{pair[0]}: {len(a)} entries, {pair[1]}: {len(b)} entries -> {'EQUAL' if a == b else 'DIFFERENT'}")
for app in ("fpm_demo_base", "fpm_demo_child"):
    a = load("offline-assets.json")
    for key in (f"{app}.bundle.js", f"{app}.bundle.css"):
        if key not in a: problems.append(f"missing {key} in offline assets.json")
    if f"rtl_{app}.bundle.css" not in load("offline-assets-rtl.json"): problems.append(f"missing rtl_{app}.bundle.css")
if problems:
    print("\n".join(problems)); sys.exit(1)
EOF
	pass "sites/assets/assets.json and assets-rtl.json equal (keys and values) a live 'bench build --apps …' of the same apps, and are JSON.stringify(_, null, 4) shaped"

	# --- 5. the site serves the apps' JS/CSS over HTTP ---
	http_check
	pass "site serves /login and /app with the apps' hashed bundles resolved through assets.json; each bundle returns 200 with its marker"

	{
		echo; echo "## Packages"; ls -la "$OUT"/*.fpm
		echo; echo "## fpm_demo_base lock"; cat "$OUT/fpm_demo_base.lock"
		echo; echo "## fpm exists (commit + platform + python)"; cat "$OUT/exists-base.json"
		echo; echo "## fpm deps --json (child)"; cat "$OUT/deps-child.json"
		echo; echo "## offline assets.json"; cat "$OUT/offline-assets.json"; echo
		echo; echo "## served assets"; cat "$OUT/http-assets.txt"
		echo; echo "## network isolation"; cat "$OUT/network-isolation.log"
	} >> "$OUT/RESULT.md"
	log "ALL ASSERTIONS PASSED — see $OUT/RESULT.md"
}

# reference_build runs Frappe's own `bench build` for the same apps from source in a
# throwaway container of the same image (no services needed, still no network), and
# captures the manifest it produces.
reference_build() {
	log "Reference: live 'bench build --apps …' of the same apps from source"
	rm -rf "$WORK/ref-apps"; cp -r "$WORK/apps" "$WORK/ref-apps"
	find "$WORK/ref-apps" -type d \( -name dist -o -name .git \) -prune -exec rm -rf {} +
	podman run --rm "${USERNS[@]}" --network none \
		-v "$WORK/ref-apps:/work/apps:Z" -v "$OUT:/out:Z" \
		-e HOME=/home/frappe \
		"$IMAGE" bash -c '
set -euo pipefail
export PATH='"$PATH_PREFIX"':$PATH
cd '"$BENCH"'
# apps.txt may lack a trailing newline; make sure each app lands on its own line.
[ -z "$(tail -c1 sites/apps.txt)" ] || echo >> sites/apps.txt
# Real copies at apps/<app>, as `bench get-app` leaves them: esbuild folds the input
# source paths into each bundle hash, so an app reached through a symlink into a
# mounted directory would be hashed under that other path and not compare equal.
for app in '"${APPS[*]}"'; do cp -r /work/apps/$app apps/$app; echo $app >> sites/apps.txt; done
cd sites
PYTHONPATH=/work/apps/fpm_demo_base:/work/apps/fpm_demo_child:/work/apps/fpm_demo_plain FRAPPE_DOCKER_BUILD=1 \
  ../env/bin/python -m frappe.utils.bench_helper frappe build --apps '"$(IFS=,; echo "${APPS[*]}")"' --production
cp assets/assets.json /out/reference-assets.json
cp assets/assets-rtl.json /out/reference-assets-rtl.json
' 2>&1 | tee "$OUT/reference-build.log"
}

http_check() {
	start_web
	log "HTTP: pages and bundles served by the isolated site"
	# First renders can be slow (the desk boot builds its caches), so generous timeouts;
	# codes are recorded so a failure says what the server answered.
	fetch_page() { # <url-path> <out-file> [extra curl args]
		local path="$1" out="$2"; shift 2
		local code
		code=$(cexec "curl -sS -L -m 120 -H 'Host: $SITE' $* -o /tmp/page.html -w '%{http_code}' http://127.0.0.1:8000$path") \
			|| fail "GET $path failed (curl exit $?)"
		cexec "cat /tmp/page.html" > "$OUT/$out"
		echo "GET $path -> $code ($(wc -c < "$OUT/$out") bytes)" | tee -a "$OUT/http-assets.txt"
		[ "$code" = 200 ] || fail "GET $path -> HTTP $code"
	}
	: > "$OUT/http-assets.txt"
	fetch_page /login http-login.html
	cexec "curl -sS -m 60 -H 'Host: $SITE' -c /tmp/c -d 'usr=Administrator&pwd=$ADMIN_PASSWORD' http://127.0.0.1:8000/api/method/login" > "$OUT/http-login-api.json"
	grep -q '"message"' "$OUT/http-login-api.json" || fail "login as Administrator failed: $(cat "$OUT/http-login-api.json")"
	fetch_page /app http-app.html -b /tmp/c

	for app in fpm_demo_base fpm_demo_child; do
		marker="$(tr '[:lower:]' '[:upper:]' <<< "$app")_MARKER"
		# Website pages (/login) use web_include_*; the desk (/app) uses app_include_*.
		for page in http-login.html http-app.html; do
			js=$(grep -o "/assets/$app/dist/js/$app\.bundle\.[A-Z0-9]*\.js" "$OUT/$page" | head -1)
			css=$(grep -o "/assets/$app/dist/css/$app\.bundle\.[A-Z0-9]*\.css" "$OUT/$page" | head -1)
			[ -n "$js" ] || fail "$page does not reference $app's hashed JS bundle"
			[ -n "$css" ] || fail "$page does not reference $app's hashed CSS bundle"
			echo "$page -> $js $css" >> "$OUT/http-assets.txt"
		done
		js=$(grep -o "/assets/$app/dist/js/$app\.bundle\.[A-Z0-9]*\.js" "$OUT/http-app.html" | head -1)
		css=$(grep -o "/assets/$app/dist/css/$app\.bundle\.[A-Z0-9]*\.css" "$OUT/http-app.html" | head -1)
		# The referenced path is the one recorded in assets.json.
		grep -q "\"$js\"" "$OUT/offline-assets.json" || fail "$js served in HTML is not the assets.json value"
		code=$(cexec "curl -s -m 60 -H 'Host: $SITE' -o /tmp/asset.js -w '%{http_code}' http://127.0.0.1:8000$js")
		[ "$code" = 200 ] || fail "GET $js -> $code"
		cexec "grep -q $marker /tmp/asset.js" || fail "$js does not contain $marker"
		code=$(cexec "curl -s -m 60 -H 'Host: $SITE' -o /tmp/asset.css -w '%{http_code}' http://127.0.0.1:8000$css")
		[ "$code" = 200 ] || fail "GET $css -> $code"
		cexec "grep -q '$app-banner' /tmp/asset.css" || fail "$css does not contain the app's rule"
		# A non-bundled static file, served through the sites/assets/<app> link.
		code=$(cexec "curl -s -m 60 -H 'Host: $SITE' -o /dev/null -w '%{http_code}' http://127.0.0.1:8000/assets/$app/images/$app.svg")
		[ "$code" = 200 ] || fail "GET /assets/$app/images/$app.svg -> $code"
		echo "$app: js=$js css=$css svg=200 marker=$marker" >> "$OUT/http-assets.txt"
	done
	cat "$OUT/http-assets.txt"
}

# phase_ui drives a headless Chromium inside the isolated bench's network namespace
# (it can reach 127.0.0.1:8000 and nothing else) and captures screenshots plus a JSON
# report of what the apps' bundles did in the page.
CHROME_IMAGE="${FPM_OFFLINE_CHROME_IMAGE:-docker.io/zenika/alpine-chrome:with-puppeteer}"
phase_ui() {
	podman inspect "$NAME" >/dev/null 2>&1 || fail "container $NAME is not running; run the offline phase first"
	relabel_shared
	start_web
	log "Screenshots through a headless browser sharing $NAME's (network-less) namespace"
	mkdir -p "$OUT/ui"; chmod 777 "$OUT/ui"
	# The image keeps puppeteer in its own app dir; NODE_PATH lets the mounted script find it.
	podman run --rm --network "container:$NAME" \
		-v "$WORK/ui:/ui:ro,Z" -v "$OUT/ui:/out:Z" -e ADMIN_PASSWORD="$ADMIN_PASSWORD" \
		-e NODE_PATH=/usr/src/app/node_modules:/usr/lib/node_modules:/usr/local/lib/node_modules:/home/chrome/node_modules \
		"$CHROME_IMAGE" node /ui/shot.js 2>&1 | tee "$OUT/ui/shot.log"
	echo "bench container network mode: $(podman inspect "$NAME" --format '{{.HostConfig.NetworkMode}}') (browser joined it)" >> "$OUT/ui/shot.log"
	ls -la "$OUT/ui"
	grep -q 'UI CHECK PASSED' "$OUT/ui/shot.log" || fail "browser check failed"
	pass "headless browser: both apps' JS ran (markers + banners rendered), CSS applied, every /assets/fpm_demo_* response 200 — see out/ui/*.png"
}

# phase_tunnel exposes the isolated site to a browser on the LAN without giving the
# container any network: a relay in the container's network namespace forwards a UNIX
# socket (on a bind mount) to 127.0.0.1:8000, and a relay on the host listens on TCP
# and connects to that socket. UNIX sockets cross network namespaces; nothing else does.
TUNNEL_PORT="${FPM_OFFLINE_TUNNEL_PORT:-8080}"
phase_tunnel() {
	podman inspect "$NAME" >/dev/null 2>&1 || fail "container $NAME is not running"
	relabel_shared
	start_web
	mkdir -p "$WORK/tunnel"; chmod 777 "$WORK/tunnel"; rm -f "$WORK/tunnel/$NAME.sock"
	podman rm -f "$NAME-relay" >/dev/null 2>&1 || true
	podman run -d --name "$NAME-relay" --network "container:$NAME" "${USERNS[@]}" \
		-v "$WORK/ui:/ui:ro,Z" -v "$WORK/tunnel:/tunnel:Z" \
		"$IMAGE" python3 /ui/relay.py unix2tcp "/tunnel/$NAME.sock" 127.0.0.1 8000 >/dev/null
	for i in $(seq 1 30); do [ -S "$WORK/tunnel/$NAME.sock" ] && break; sleep 1; done
	[ -S "$WORK/tunnel/$NAME.sock" ] || { podman logs "$NAME-relay"; fail "relay socket did not appear"; }
	pkill -f "relay.py tcp2unix $TUNNEL_PORT " >/dev/null 2>&1 || true
	nohup python3 "$WORK/ui/relay.py" tcp2unix "$TUNNEL_PORT" "$WORK/tunnel/$NAME.sock" > "$WORK/tunnel/host-relay-$TUNNEL_PORT.log" 2>&1 &
	sleep 1
	curl -s -m 30 "http://127.0.0.1:$TUNNEL_PORT/api/method/ping" | grep -q pong || { cat "$WORK/tunnel/host-relay-$TUNNEL_PORT.log"; fail "tunnel does not answer"; }
	log "Site reachable at http://$(hostname -I 2>/dev/null | awk '{print $1}' || hostname):$TUNNEL_PORT/login  (Administrator / $ADMIN_PASSWORD) — the bench container still has NetworkMode=none"
}

# phase_real answers "if hrms depends on erpnext, can fpm pack with all deps?" with
# the real apps against the ERPNext single-node image, which already carries erpnext
# in its bench and on its site: hrms is packaged (online) with --bench-path in that
# image — its required_apps entry frappe/erpnext resolves to the erpnext *in the
# bench* — and --with-deps writes a bundle that lists erpnext as provided by the
# bench (not shipped). Offline, in a --network none container of the same image,
# `fpm install <bundle>` skips erpnext and installs hrms onto the existing site.
REAL_IMAGE="${FPM_OFFLINE_REAL_IMAGE:-docker.io/vyogo/erpnext:sne-develop}"
REAL_NAME="${FPM_OFFLINE_REAL_CONTAINER:-fpm-offline-real}"
REAL_SITE="${FPM_OFFLINE_REAL_SITE:-dev.localhost}"
# hrms must match the Frappe/ERPNext inside the image (built 2026-08-12): this is the
# last develop commit before that. Override to test another.
HRMS_REF="${FPM_OFFLINE_HRMS_REF:-8c1d3277f872740c03669c92b410957453c9fa41}"
phase_real() {
	IMAGE="$REAL_IMAGE"; NAME="$REAL_NAME"
	podman image exists "$IMAGE" || { log "Pulling $IMAGE"; podman pull -q "$IMAGE"; }
	[ -n "$PYTHON_VERSION" ] || PYTHON_VERSION="$(image_python_version)"
	mkdir -p "$OUT/real" "$WORK/real" "$WORK/builder-home/.fpm"
	chmod 777 "$OUT/real" "$WORK/real"
	local flags
	flags="--output-path /out/real --bench-path $BENCH --python-version $PYTHON_VERSION $(platform_flags)"
	if [ "${FPM_OFFLINE_REAL_REUSE:-}" = 1 ] && [ -e "$OUT/real/hrms-bundle" ]; then
		log "Reusing already-built hrms package in $OUT/real (FPM_OFFLINE_REAL_REUSE=1)"
	else
	log "Online: fetch + package hrms inside $IMAGE (erpnext is already in that bench)"
	rm -rf "$OUT/real"/*.fpm "$OUT/real"/*-bundle "$OUT/real/hrms-bundle" "$WORK/builder-home/.fpm/apps/frappe"
	podman run --rm "${USERNS[@]}" \
		-v "$WORK/real:/work/real:Z" -v "$OUT/real:/out/real:Z" -v "$WORK/bin:/opt/fpm:ro,Z" \
		-v "$WORK/builder-home/.fpm:/home/frappe/.fpm:Z" -e HOME=/home/frappe \
		"$IMAGE" bash -c '
set -euo pipefail
export PATH='"$PATH_PREFIX"':$PATH
cd /work/real
appver() { python3 -c "import re,sys; print(re.search(r\"__version__\\s*=\\s*[\\\"\x27]([^\\\"\x27]+)\", open(sys.argv[1]).read()).group(1))" "$1"; }
fetch_app() { # <app> <ref>: a shallow checkout of exactly that commit
  local app="$1" ref="$2"
  if [ -d "$app" ] && [ "$(git -C "$app" rev-parse HEAD 2>/dev/null)" = "$ref" ]; then return; fi
  rm -rf "$app"; mkdir -p "$app"
  git -C "$app" init -q && git -C "$app" remote add origin "https://github.com/frappe/$app.git"
  git -C "$app" fetch -q --depth 1 origin "$ref" && git -C "$app" checkout -q FETCH_HEAD
}
fetch_app hrms '"$HRMS_REF"'
: > /out/real/versions.txt
echo "image erpnext: $(appver '"$BENCH"'/apps/erpnext/erpnext/__init__.py)  image frappe: $(appver '"$BENCH"'/apps/frappe/frappe/__init__.py)" | tee -a /out/real/versions.txt
echo "hrms: $(git -C hrms rev-parse HEAD) ($(git -C hrms log -1 --format=%cd --date=short)) version $(appver hrms/hrms/__init__.py)" | tee -a /out/real/versions.txt
HV=$(appver hrms/hrms/__init__.py)
echo "=== package hrms $HV --with-deps: required_apps=[frappe/erpnext] resolves to the erpnext in this bench"
fpm package hrms --org frappe --version "$HV" --with-deps '"$flags"' 2>&1 | tee /out/real/package-hrms.log | grep -v "^\s*$" | tail -40
ln -sfn "hrms-$HV-bundle" /out/real/hrms-bundle
ls -la /out/real "/out/real/hrms-$HV-bundle"
cat "/out/real/hrms-$HV-bundle/fpm-bundle.json"
fpm deps "/out/real/hrms-$HV.fpm" --bench-path '"$BENCH"' > /out/real/deps-hrms.txt; cat /out/real/deps-hrms.txt
'
	fi
	grep -q '"provided_by": "bench"' "$OUT/real/hrms-bundle/fpm-bundle.json" || fail "bundle does not mark erpnext as provided by the bench"
	grep -q 'bench:' "$OUT/real/package-hrms.log" || fail "hrms's required erpnext was not resolved from the bench"
	pass "REAL APPS (online): hrms packaged in the ERPNext image; required_apps frappe/erpnext pinned to the bench's erpnext; --with-deps bundle lists erpnext as provided by the bench and ships only hrms"

	log "Offline: $IMAGE with --network none (erpnext already on $REAL_SITE), then 'fpm install <hrms bundle>'"
	if [ "${FPM_OFFLINE_REAL_KEEP:-}" = 1 ] && [ "$(podman inspect "$NAME" --format '{{.State.Running}}' 2>/dev/null)" = true ]; then
		log "Keeping the running container $NAME (FPM_OFFLINE_REAL_KEEP=1)"
	else
		container_up
	fi
	prove_isolation
	relabel_shared
	# A fresh log file: a writer left over from an interrupted run must not share it.
	rm -f "$OUT/real/install-bundle.log"
	cexec "fpm install /out/real/hrms-bundle --bench-path $BENCH --site $REAL_SITE" 2>&1 | tee "$OUT/real/install-bundle.log"
	grep -q '\[1/2\] frappe/erpnext==.* provided by the bench, not reinstalled' "$OUT/real/install-bundle.log" || fail "erpnext was not recognised as provided by the bench"
	grep -q '\[2/2\] frappe/hrms==' "$OUT/real/install-bundle.log" || fail "hrms was not installed second"
	grep -q 'Required app frappe/erpnext==.* provided by the bench' "$OUT/real/install-bundle.log" || fail "hrms pre-check did not accept the bench's erpnext"
	grep -q -- '--no-index --find-links' "$OUT/real/install-bundle.log" || fail "pip did not run offline"
	if grep -Ei 'esbuild|yarn|bench build|Building wheel for|Running setup\.py' "$OUT/real/install-bundle.log"; then fail "bundle install performed a build"; fi
	cexec "cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site $REAL_SITE list-apps" > "$OUT/real/list-apps.txt"
	grep -q erpnext "$OUT/real/list-apps.txt" && grep -q hrms "$OUT/real/list-apps.txt" || fail "erpnext/hrms not installed on $REAL_SITE"
	cexec "cat sites/assets/assets.json" > "$OUT/real/assets.json"
	grep -q '"hrms.bundle.js"' "$OUT/real/assets.json" && grep -q '"erpnext.bundle.js"' "$OUT/real/assets.json" || fail "erpnext/hrms bundles missing from assets.json"
	pass "REAL APPS (offline): hrms bundle installed onto $REAL_SITE of the ERPNext image with no network — erpnext recognised as provided by the bench and NOT reinstalled, hrms from vendored wheels, no build at install; assets.json has erpnext.bundle.js + hrms.bundle.js"
	echo "- site apps: $(tr '\n' ' ' < "$OUT/real/list-apps.txt")" >> "$OUT/RESULT.md"
	cat "$OUT/real/versions.txt" >> "$OUT/RESULT.md"
}

phase_clean() {
	podman rm -f "$NAME" "$NAME-relay" >/dev/null 2>&1 || true
	pkill -f "relay.py tcp2unix" >/dev/null 2>&1 || true
	if [ "${CLEAN_ALL:-0}" = 1 ]; then rm -rf "$OUT" "$WORK/builder-home" "$WORK/ref-apps" "$WORK/tunnel"; fi
	echo "cleaned"
}

case "${1:-}" in
	image)   phase_image ;;
	package) phase_package ;;
	offline) phase_offline ;;
	verify)  : > "$OUT/RESULT.md"; phase_verify ;;
	ui)      phase_ui ;;
	tunnel)  phase_tunnel ;;
	real)    : > "$OUT/RESULT.md"; phase_real ;;
	clean)   phase_clean ;;
	*) echo "usage: remote.sh image|package|offline|verify|ui|tunnel|real|clean" >&2; exit 2 ;;
esac
