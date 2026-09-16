#!/usr/bin/env python3
"""Remove published versions from the HTTP registry, index included.

Deleting the .fpm object is only half the job. The registry's DELETE handler removes
exactly the key it is given and nothing else, so package-metadata.json goes on listing
a version whose artifact is gone — and an advertised version that 404s is worse than
the bad package it replaced. Every removal here is therefore: drop the object, rewrite
the index, then prove both.

The one invariant that keeps the rewrite safe: a version that is the app's current
latest_version is refused. With that guaranteed, dropping a version can never change
which version latest_version names, so the index rewrite is a key deletion and never a
re-resolution — no semver ordering is reimplemented here to disagree with the CLI's.
"""

import json
import os
import sys
import urllib.error
import urllib.request

# Cloudflare answers the default urllib agent with a 403, which reads exactly like an
# auth failure and cost a previous session a wrong conclusion. Send a real one.
AGENT = "fpm-registry-cleanup"


def request(method, url, token=None, user=None, body=None):
    req = urllib.request.Request(url, method=method, data=body)
    req.add_header("User-Agent", AGENT)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if body is not None:
        req.add_header("Content-Type", "application/json; charset=utf-8")
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as err:
        return err.code, err.read()


def metadata_url(base, app):
    return f"{base}/metadata/frappe/{app}/package-metadata.json"


def main():
    base = os.environ.get("REGISTRY_URL", "").rstrip("/")
    token = os.environ.get("REGISTRY_TOKEN", "")
    dry_run = os.environ.get("DRY_RUN", "true").lower() == "true"
    raw = os.environ.get("VERSIONS", "").strip()

    if not base:
        sys.exit("FPM_REGISTRY_URL is not configured for this repository.")
    if not token:
        sys.exit("FPM_REGISTRY_PASSWORD is not set; it is the registry's publisher token.")
    if not raw:
        sys.exit("No versions given.")

    targets = []
    for chunk in raw.split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        if "==" not in chunk:
            sys.exit(f"{chunk!r} is not <app>==<version>.")
        app, version = chunk.split("==", 1)
        targets.append((app.strip(), version.strip()))

    # Everything is checked before anything is deleted, so a bad entry halfway down the
    # list cannot leave the registry half-cleaned.
    plan = []
    for app, version in targets:
        status, raw_meta = request("GET", metadata_url(base, app))
        if status != 200:
            sys.exit(f"{app}: metadata returned HTTP {status}; is the app published?")
        meta = json.loads(raw_meta)
        versions = meta.get("versions", {})

        if version not in versions:
            sys.exit(f"{app}=={version} is not published; nothing to remove.")
        if meta.get("latest_version") == version:
            sys.exit(
                f"{app}=={version} is the current latest_version. Removing it would change "
                f"what every consumer resolves to, which is not what this workflow is for."
            )

        entry = versions[version]
        plan.append(
            {
                "app": app,
                "version": version,
                "path": entry["fpm_path"],
                "wheels": entry.get("wheel_platform") or "NONE",
                "meta": meta,
            }
        )

    print(f"{'APP':<14} {'VERSION':<36} {'WHEELS':<22} OBJECT")
    print("-" * 100)
    for item in plan:
        print(f"{item['app']:<14} {item['version']:<36} {item['wheels']:<22} {item['path']}")
    print()

    if dry_run:
        print(f"Dry run: {len(plan)} version(s) would be removed. Nothing was changed.")
        return

    failed = False
    for item in plan:
        app, version = item["app"], item["version"]
        print(f"--- {app}=={version}")

        status, body = request("DELETE", f"{base}/{item['path']}", token=token)
        if status != 200:
            print(f"  delete failed: HTTP {status} {body[:200]!r}")
            failed = True
            continue
        print(f"  object deleted")

        # Re-read rather than reusing the copy from the planning pass: a mirror run
        # between the two would otherwise be silently reverted by this write.
        status, raw_meta = request("GET", metadata_url(base, app))
        if status != 200:
            print(f"  could not re-read metadata: HTTP {status}")
            failed = True
            continue
        meta = json.loads(raw_meta)
        if meta.get("latest_version") == version:
            print(f"  latest_version now names {version}; refusing to rewrite the index")
            failed = True
            continue
        meta.get("versions", {}).pop(version, None)

        payload = json.dumps(meta, indent=2).encode()
        status, body = request("PUT", metadata_url(base, app), token=token, body=payload)
        if status != 200:
            print(f"  index rewrite failed: HTTP {status} {body[:200]!r}")
            failed = True
            continue
        print(f"  index rewritten")

    print("\nVerifying...")
    for item in plan:
        app, version = item["app"], item["version"]
        status, _ = request("GET", f"{base}/{item['path']}")
        object_gone = status == 404
        status, raw_meta = request("GET", metadata_url(base, app))
        listed = status == 200 and version in json.loads(raw_meta).get("versions", {})
        ok = object_gone and not listed
        print(
            f"  {'ok ' if ok else 'BAD'} {app}=={version}: "
            f"object {'gone' if object_gone else f'still served (HTTP {status})'}, "
            f"index {'clean' if not listed else 'STILL LISTS IT'}"
        )
        failed = failed or not ok

    if failed:
        sys.exit("Cleanup did not fully succeed.")
    print(f"\n{len(plan)} version(s) removed.")


if __name__ == "__main__":
    main()
