# Mirror catalog

`apps.csv` is the single source of truth for what `fpm mirror` builds and
publishes. Only repositories under the official `frappe` GitHub organisation
are accepted — the loader rejects anything else, so this registry never gives a
third-party app a listing. Adding an app is a reviewed change to this file.

Run it with:

```bash
fpm mirror --catalog catalog/apps.csv --repo <repo-name> [--apps crm,hrms] [--dry-run]
```

## Columns

Empty cells take the default.

| column | default | meaning |
|---|---|---|
| `slug` | — | unique key; used for `--apps` filtering, workspace directories, and reporting |
| `repo` | — | must start with `https://github.com/frappe/` |
| `app_name` | derived | overrides the app name when `hooks.py app_name` differs from the repo name |
| `track` | `tags` | `tags`: latest release tag per major line; `branch`: tip of one branch |
| `branch` | — | only with `track=branch` |
| `branch_major` | `0` | major used in the branch pseudo-version `<major>.0.0-git.<date>.<sha>` (a prerelease, so it never becomes `latest_version`) |
| `majors` | all tagged | semicolon-separated allowlist of major lines, e.g. `14;15;16` |
| `bundle_deps` | `true` | set `false` for apps whose Python deps ship no wheels (`pip download --only-binary` would fail) |
| `enabled` | `true` | set `false` to keep an app listed but unbuilt; say why in `notes` |
| `notes` | — | free text; quote the cell if it contains commas |

## Asset builds — `build/<slug>.sh`

fpm never builds JS assets; it only packages a `compiled_assets/` directory
that already exists in the source. If `catalog/build/<slug>.sh` exists, mirror
runs it in the checkout root before packaging, with `npm_config_cache` and
`YARN_CACHE_FOLDER` pointed at the shared build cache. The script's contract:
**leave a `compiled_assets/` directory at the repo root**. A failing build
script fails that app (isolated and reported) — there is no silent fallback to
a source-only package once a script exists. Apps without a script publish
source-only packages; `fpm install` then skips asset deployment and the bench
builds assets as usual.

## Licensing

The mirrored apps are GPL/AGPL-licensed. Each `.fpm` artifact is the complete,
unmodified upstream source for the tagged release, LICENSE files included, so
republishing them here is ordinary source redistribution.
