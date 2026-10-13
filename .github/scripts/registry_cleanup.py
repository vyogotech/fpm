#!/usr/bin/env python3
"""Remove published versions, or whole apps, from the HTTP registry, index included.

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
import re
import sys
import time
import urllib.error
import urllib.request

# Cloudflare answers the default urllib agent with a 403, which reads exactly like an
# auth failure and cost a previous session a wrong conclusion. Send a real one.
AGENT = "fpm-registry-cleanup"


def request(method, url, token=None, body=None, if_match=None, attempts=4):
    """One HTTP call; an HTTP error is an answer, a transport error is retried.

    Every call opens a fresh TLS connection to the edge, and one of those failed mid-
    handshake in a run that had just deleted an artifact, leaving its metadata still
    advertising it. A DELETE and a conditional PUT are both safe to repeat, so a
    connection that fails before an answer arrives is tried again, not reported.
    """
    req = urllib.request.Request(url, method=method, data=body)
    req.add_header("User-Agent", AGENT)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if body is not None:
        req.add_header("Content-Type", "application/json; charset=utf-8")
    if if_match:
        req.add_header("If-Match", if_match)
    for attempt in range(1, attempts + 1):
        try:
            with urllib.request.urlopen(req, timeout=60) as response:
                return response.status, response.read(), response.headers.get("ETag")
        except urllib.error.HTTPError as err:
            return err.code, err.read(), err.headers.get("ETag")
        except (urllib.error.URLError, OSError) as err:
            if attempt == attempts:
                raise
            print(f"  {method} {url}: {err}; retrying ({attempt}/{attempts - 1})")
            time.sleep(2 * attempt)


INDEX_PATH = "metadata/index.json"


def metadata_url(base, app, org="frappe"):
    return f"{base}/metadata/{org}/{app}/package-metadata.json"


def parse_target(chunk):
    """[<org>/]<app>==<version> -> (org, app, version); the org defaults to frappe."""
    if "==" not in chunk:
        sys.exit(f"{chunk!r} is not [<org>/]<app>==<version>.")
    name, version = chunk.split("==", 1)
    org, _, app = name.strip().rpartition("/")
    return (org or "frappe"), app.strip(), version.strip()


def artifact_paths(raw_meta):
    """Every object a package-metadata.json names, read without a JSON parser.

    Only the keys matter here, and reading them this way still works on a metadata
    file that has been damaged, which is one more thing a cleanup may be clearing up.
    """
    return sorted(set(re.findall(r'"fpm_path"\s*:\s*"([^"]+)"', raw_meta.decode("utf-8", "replace"))))


def remove_app(base, org, app, token, dry_run):
    """Delete every artifact of org/app, its package metadata, and its index entry."""
    meta_url = metadata_url(base, app, org)
    status, raw_meta, _ = request("GET", meta_url)
    if status != 200:
        sys.exit(f"{org}/{app}: metadata returned HTTP {status}; is the app published?")
    paths = artifact_paths(raw_meta)

    print(f"{org}/{app}: {len(paths)} artifact(s), the package metadata, and its index entry")
    for path in paths:
        print(f"  {path}")
    if dry_run:
        return True

    ok = True
    for path in paths:
        status, body, _ = request("DELETE", f"{base}/{path}", token=token)
        if status in (200, 404):
            print(f"  deleted {path}" if status == 200 else f"  already gone {path}")
        else:
            print(f"  delete failed: {path}: HTTP {status} {body[:200]!r}")
            ok = False
    status, body, _ = request("DELETE", f"{base}/metadata/{org}/{app}/package-metadata.json", token=token)
    if status in (200, 404):
        print("  package metadata deleted")
    else:
        print(f"  metadata delete failed: HTTP {status} {body[:200]!r}")
        ok = False
    if not drop_index_entry(base, org, app, token):
        ok = False
    return ok


def drop_index_entry(base, org, app, token, attempts=4):
    """Remove org/app from metadata/index.json, conditional on the index not having moved."""
    for attempt in range(attempts):
        status, raw, etag = request("GET", f"{base}/{INDEX_PATH}")
        if status == 404:
            return True
        if status != 200:
            print(f"  could not read the index: HTTP {status}")
            return False
        index = json.loads(raw)
        packages = index.get("packages") or []
        kept = [e for e in packages if not (e.get("org") == org and e.get("appName") == app)]
        if len(kept) == len(packages):
            print("  index has no entry for it")
            return True
        index["packages"] = kept
        status, body, _ = request(
            "PUT", f"{base}/{INDEX_PATH}", token=token, body=json.dumps(index, indent=2).encode(), if_match=etag
        )
        if status == 200:
            print("  index entry removed")
            return True
        if status == 412:
            print(f"  index changed under us, retrying ({attempt + 1}/{attempts})")
            continue
        print(f"  index rewrite failed: HTTP {status} {body[:200]!r}")
        return False
    print(f"  index kept changing; gave up after {attempts} attempts")
    return False


def verify_app_gone(base, org, app, paths):
    ok = True
    for path in paths:
        status, _, _ = request("GET", f"{base}/{path}")
        if status != 404:
            print(f"  BAD {path} still served (HTTP {status})")
            ok = False
    status, _, _ = request("GET", metadata_url(base, app, org))
    if status != 404:
        print(f"  BAD metadata still served (HTTP {status})")
        ok = False
    status, raw, _ = request("GET", f"{base}/{INDEX_PATH}")
    if status == 200 and any(
        e.get("org") == org and e.get("appName") == app for e in (json.loads(raw).get("packages") or [])
    ):
        print("  BAD index still lists it")
        ok = False
    print(f"  {'ok ' if ok else 'BAD'} {org}/{app} removed")
    return ok


def rewrite_index(base, app, version, token, org="frappe", attempts=4):
    """Drop a version from package-metadata.json, conditional on it not having moved."""
    for attempt in range(attempts):
        status, raw_meta, etag = request("GET", metadata_url(base, app, org))
        if status != 200:
            print(f"  could not re-read metadata: HTTP {status}")
            return False
        meta = json.loads(raw_meta)
        if meta.get("latest_version") == version:
            print(f"  latest_version now names {version}; refusing to rewrite the index")
            return False
        if version not in meta.get("versions", {}):
            return True  # someone else already removed it
        meta["versions"].pop(version, None)

        payload = json.dumps(meta, indent=2).encode()
        status, body, _ = request(
            "PUT", metadata_url(base, app, org), token=token, body=payload, if_match=etag
        )
        if status == 200:
            return True
        if status == 412:
            print(f"  index changed under us, retrying ({attempt + 1}/{attempts})")
            continue
        print(f"  index rewrite failed: HTTP {status} {body[:200]!r}")
        return False
    print(f"  index kept changing; gave up after {attempts} attempts")
    return False


def main():
    base = os.environ.get("REGISTRY_URL", "").rstrip("/")
    token = os.environ.get("REGISTRY_TOKEN", "")
    dry_run = os.environ.get("DRY_RUN", "true").lower() == "true"
    raw = os.environ.get("VERSIONS", "").strip()
    raw_apps = os.environ.get("APPS", "").strip()

    if not base:
        sys.exit("FPM_REGISTRY_URL is not configured for this repository.")
    if not token:
        sys.exit("FPM_REGISTRY_PASSWORD is not set; it is the registry's publisher token.")
    if not raw and not raw_apps:
        sys.exit("No versions and no apps given.")

    apps = []
    for chunk in raw_apps.split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        org, _, app = chunk.rpartition("/")
        if not org or not app:
            sys.exit(f"{chunk!r} is not <org>/<app>.")
        apps.append((org, app))
    if apps:
        failed = False
        removed = []
        for org, app in apps:
            status, raw_meta, _ = request("GET", metadata_url(base, app, org))
            paths = artifact_paths(raw_meta) if status == 200 else []
            if not remove_app(base, org, app, token, dry_run):
                failed = True
            removed.append((org, app, paths))
        if dry_run:
            print(f"\nDry run: {len(apps)} app(s) would be removed entirely. Nothing was changed.")
        else:
            print("\nVerifying...")
            for org, app, paths in removed:
                failed = not verify_app_gone(base, org, app, paths) or failed
        if failed:
            sys.exit("App removal did not fully succeed.")
        if not raw:
            return

    targets = []
    for chunk in raw.split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        targets.append(parse_target(chunk))

    # Everything is checked before anything is deleted, so a bad entry halfway down the
    # list cannot leave the registry half-cleaned.
    plan = []
    for org, app, version in targets:
        status, raw_meta, _ = request("GET", metadata_url(base, app, org))
        if status != 200:
            sys.exit(f"{org}/{app}: metadata returned HTTP {status}; is the app published?")
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
                "org": org,
                "app": app,
                "version": version,
                "path": entry["fpm_path"],
                "wheels": entry.get("wheel_platform") or "NONE",
                "meta": meta,
            }
        )

    print(f"{'APP':<24} {'VERSION':<36} {'WHEELS':<22} OBJECT")
    print("-" * 110)
    for item in plan:
        print(f"{item['org'] + '/' + item['app']:<24} {item['version']:<36} {item['wheels']:<22} {item['path']}")
    print()

    if dry_run:
        print(f"Dry run: {len(plan)} version(s) would be removed. Nothing was changed.")
        return

    failed = False
    for item in plan:
        org, app, version = item["org"], item["app"], item["version"]
        print(f"--- {org}/{app}=={version}")

        status, body, _ = request("DELETE", f"{base}/{item['path']}", token=token)
        if status != 200:
            print(f"  delete failed: HTTP {status} {body[:200]!r}")
            failed = True
            continue
        print(f"  object deleted")

        # The index is read-modify-written, so it is re-read here rather than reused
        # from the planning pass, and written back conditionally on the ETag that read
        # returned. A mirror publish landing in between changes the ETag, the registry
        # answers 412, and this retries against the document that publish left --
        # rather than overwriting it and erasing the version it had just added.
        if not rewrite_index(base, app, version, token, org):
            failed = True
            continue
        print(f"  index rewritten")

    print("\nVerifying...")
    for item in plan:
        org, app, version = item["org"], item["app"], item["version"]
        status, _, _ = request("GET", f"{base}/{item['path']}")
        object_gone = status == 404
        status, raw_meta, _ = request("GET", metadata_url(base, app, org))
        listed = status == 200 and version in json.loads(raw_meta).get("versions", {})
        ok = object_gone and not listed
        print(
            f"  {'ok ' if ok else 'BAD'} {org}/{app}=={version}: "
            f"object {'gone' if object_gone else f'still served (HTTP {status})'}, "
            f"index {'clean' if not listed else 'STILL LISTS IT'}"
        )
        failed = failed or not ok

    if failed:
        sys.exit("Cleanup did not fully succeed.")
    print(f"\n{len(plan)} version(s) removed.")


if __name__ == "__main__":
    main()
