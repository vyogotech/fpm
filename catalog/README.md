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
| `tier` | `0` | build wave; every app in a lower tier is published before a higher one starts, so an app can pin the `required_apps` this run just published (hrms needs erpnext) |
| `build_deps` | — | `<slug>@<ref>` pairs, `;`-separated, for another app's source this app's build reads off disk, e.g. `frappe@version-15`. Also selects the frappe whose esbuild compiles this app's desk assets, overriding `--frappe-ref` |
| `notes` | — | free text; quote the cell if it contains commas |

## Asset builds

Every app is published with its assets **compiled**. A package that ships sources
installs cleanly and then renders nothing, because a bench that installs from a package
never runs a build — which is what made the published front-end packages unusable
(issue #9).

Two kinds of asset, built automatically:

- **Desk bundles** — `<app>/public/**/*.bundle.{js,ts,css,scss}`, compiled by frappe's
  own esbuild into `<app>/public/dist/`. The workspace is laid out as a bench, so mirror
  materialises frappe's checkout beside the app and packages against it. The frappe ref
  comes from this app's `build_deps` pin, else `--frappe-ref` (default `version-16`).
- **App frontends** — the Vite SPA that crm, helpdesk, insights and friends build into
  `<app>/public/frontend`, compiled by `fpm package` itself.

An app whose assets cannot be compiled **fails** — isolated and reported — rather than
publishing a package that installs and serves nothing. `fpm mirror --allow-unbuilt-assets`
publishes it anyway when that is the deliberate choice.

### Escape hatch — `build/<slug>.sh`

If `catalog/build/<slug>.sh` exists, mirror runs it in the checkout root before
packaging instead of the automatic build, with `npm_config_cache` and
`YARN_CACHE_FOLDER` pointed at the shared build cache. The script's contract:
**leave a `compiled_assets/` directory at the repo root**. A failing build script fails
that app.

## Licensing

The mirrored apps are GPL/AGPL-licensed. Each `.fpm` artifact is the complete,
unmodified upstream source for the tagged release, LICENSE files included, so
republishing them here is ordinary source redistribution.
