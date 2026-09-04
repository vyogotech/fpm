package cmd

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"fpm/internal/metadata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crmShapedApp builds the checkout layout of frappe/crm: a delegating root
// package.json, the Vite project in frontend/, and a python module whose
// public/frontend and www/crm.html do not exist yet because the app's .gitignore
// lists them as build outputs.
func crmShapedApp(t *testing.T, appName string) string {
	t.Helper()
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), appName, map[string]string{
		"package.json": `{
		  "private": true,
		  "name": "` + appName + `",
		  "scripts": {
		    "postinstall": "cd frontend && yarn install --check-files",
		    "build": "cd frontend && yarn build"
		  }
		}`,
		"yarn.lock": "# yarn lockfile v1\n",
		"frontend/package.json": `{
		  "name": "` + appName + `-ui",
		  "scripts": {
		    "build": "vite build --base=/assets/` + appName + `/frontend/ && yarn copy-html-entry",
		    "copy-html-entry": "cp ../` + appName + `/public/frontend/index.html ../` + appName + `/www/` + appName + `.html"
		  }
		}`,
		appName + "/hooks.py": `app_name = "` + appName + `"
website_route_rules = [
    {"from_route": "/` + appName + `/<path:app_path>", "to_route": "` + appName + `"},
]
`,
		"frontend/yarn.lock":                 "# yarn lockfile v1\n",
		"frontend/src/main.js":               "import {createApp} from 'vue'\n",
		"frontend/node_modules/vue/index.js": "// a build-time input, never packaged\n",
		appName + "/public/.gitkeep":         "",
		appName + "/www/" + appName + ".py":  "import frappe\n",
	})
	return src
}

// spaHTML is what a built Vite index.html looks like: it links its bundles by the
// /assets/<app>/<dir>/ URL the sites/assets symlink serves.
func spaHTML(app, dir string) string {
	return "<!doctype html><script type=module src=/assets/" + app + "/" + dir +
		"/assets/index-A1B2C3.js></script><div id=app></div>"
}

// fakeYarn puts a `yarn` on PATH that logs its invocations and, on build, writes the
// files a Vite build of a frappe-ui app produces. It lets the packaging path be
// exercised end to end without node installed.
//
// Outputs are written relative to the build's working directory, not to a fixed root,
// because that is what a real build does — and because the working directory is not
// always the checkout: when the app has to be built inside a bench, `fpm package` stages
// it at <bench>/apps/<app> first and the build runs there.
func fakeYarn(t *testing.T, outputs map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake package manager is a POSIX shell script")
	}
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "invocations.log")

	var writes strings.Builder
	for rel, content := range outputs {
		writes.WriteString("mkdir -p \"$PWD/" + filepath.Dir(rel) + "\"\n")
		writes.WriteString("printf '%s' '" + content + "' > \"$PWD/" + rel + "\"\n")
	}

	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + logPath + "\"\n" +
		"case \"$1\" in install|ci) exit 0 ;; esac\n" +
		writes.String() +
		"exit 0\n"

	require.NoError(t, os.WriteFile(filepath.Join(binDir, "yarn"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// TestPackageBuildsAndShipsTheSPAFrontend is the frappe/crm case: the compiled SPA
// and the www route template are gitignored build outputs that frappe's esbuild never
// produces, so `fpm package` has to build them or the package installs cleanly and
// then serves a blank page.
func TestPackageBuildsAndShipsTheSPAFrontend(t *testing.T) {
	src := crmShapedApp(t, "crm")
	fakeYarn(t, map[string]string{
		"crm/public/frontend/index.html":             spaHTML("crm", "frontend"),
		"crm/public/frontend/assets/index-A1B2C3.js": "console.log(1)",
		"crm/www/crm.html":                           spaHTML("crm", "frontend"),
	})

	args, outDir := packageArgs(t, src, "1.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)
	fpmPath := filepath.Join(outDir, "crm-1.0.0.fpm")

	meta, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
	require.NoError(t, err)
	assert.True(t, meta.FrontendBuilt, "the package must record that it carries a compiled frontend")
	assert.Equal(t, []string{"crm/public/frontend"}, meta.FrontendDirs)
	assert.Equal(t, []string{"crm/www/crm.html"}, meta.FrontendRoutes)
	assert.Equal(t, ".", meta.FrontendSource, "crm's root package.json delegates, so the root is the build entry point")

	inspectFPM(t, fpmPath, func(filesInArchive map[string]*zip.File, _ *zip.ReadCloser) {
		for _, want := range []string{
			"crm/public/frontend/index.html",
			"crm/public/frontend/assets/index-A1B2C3.js",
			"crm/www/crm.html",
		} {
			assert.Contains(t, filesInArchive, want, "%s must be in the package; without it the SPA 404s", want)
		}
		for f := range filesInArchive {
			assert.NotContains(t, f, "node_modules",
				"node_modules is a build-time input and must never be packaged: %s", f)
		}
	})
}

// TestPackageRunsTheFrontendBuildExactlyOnce guards the double-build trap: crm's root
// build script is `cd frontend && yarn build`, so treating the root and frontend/ as
// two projects would run the same Vite build twice.
func TestPackageRunsTheFrontendBuildExactlyOnce(t *testing.T) {
	src := crmShapedApp(t, "crm")
	logPath := fakeYarn(t, map[string]string{
		"crm/public/frontend/index.html": spaHTML("crm", "frontend"),
		"crm/www/crm.html":               spaHTML("crm", "frontend"),
	})

	args, _ := packageArgs(t, src, "1.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	require.Len(t, lines, 2, "expected one install and one build, got:\n%s", log)
	assert.Contains(t, lines[0], "install")
	assert.Equal(t, "build", lines[1])
}

// TestPackageWritesTheSPARouteWhenTheBuildScriptDoesNot covers an app whose build
// script stops at `vite build`. Without <app>/www/<app>.html frappe has no route to
// render the SPA at, so fpm supplies the template crm's copy-html-entry would have.
func TestPackageWritesTheSPARouteWhenTheBuildScriptDoesNot(t *testing.T) {
	src := crmShapedApp(t, "insights")
	fakeYarn(t, map[string]string{
		"insights/public/frontend/index.html": spaHTML("insights", "frontend"),
	})

	args, outDir := packageArgs(t, src, "1.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	meta, err := metadata.ReadMetadataFromFPMArchive(filepath.Join(outDir, "insights-1.0.0.fpm"))
	require.NoError(t, err)
	assert.Equal(t, []string{"insights/www/insights.html"}, meta.FrontendRoutes)

	inspectFPM(t, filepath.Join(outDir, "insights-1.0.0.fpm"), func(filesInArchive map[string]*zip.File, _ *zip.ReadCloser) {
		assert.Contains(t, filesInArchive, "insights/www/insights.html",
			"fpm must write the route template the build script skipped")
	})
}

// TestPackageFailsWhenTheFrontendBuildProducesNothing is the whole point of the
// feature: a silent no-op build yields a package that installs and then serves a
// blank page, which is worse than a failed packaging run.
func TestPackageFailsWhenTheFrontendBuildProducesNothing(t *testing.T) {
	src := crmShapedApp(t, "crm")
	fakeYarn(t, nil)

	args, _ := packageArgs(t, src, "1.0.0", "--org", "frappe")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.Error(t, err, "packaging must fail rather than ship an empty frontend")
	assert.Contains(t, err.Error(), "blank page")
}

// TestPackageSkipsTheFrontendBuildOnRequest keeps the escape hatch working for a
// caller that builds the frontend itself or deliberately ships without it.
func TestPackageSkipsTheFrontendBuildOnRequest(t *testing.T) {
	src := crmShapedApp(t, "crm")
	logPath := fakeYarn(t, nil)

	args, outDir := packageArgs(t, src, "1.0.0", "--org", "frappe", "--build-frontend=false")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr), "no package manager should have run")
	assert.Contains(t, out, "Skipping the frontend build", "the user must be told the package has no frontend")

	meta, err := metadata.ReadMetadataFromFPMArchive(filepath.Join(outDir, "crm-1.0.0.fpm"))
	require.NoError(t, err)
	assert.False(t, meta.FrontendBuilt)
}

// TestPackageIgnoresAppsWithoutAFrontend keeps classic apps on their existing path:
// no package manager runs and no frontend metadata is recorded.
func TestPackageIgnoresAppsWithoutAFrontend(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "classic", map[string]string{
		"classic/public/js/classic.bundle.js": "// an esbuild entry point, not an SPA",
	})
	logPath := fakeYarn(t, nil)

	// --allow-unbuilt-assets: this app has an esbuild entry point and no bench to
	// compile it, which a prod package otherwise refuses (see
	// TestPackageRefusesUnbuiltDeskAssets).
	args, outDir := packageArgs(t, src, "1.0.0", "--org", "acme", "--allow-unbuilt-assets")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	_, statErr := os.Stat(logPath)
	assert.True(t, os.IsNotExist(statErr), "an app with no package.json must not invoke a package manager")

	meta, err := metadata.ReadMetadataFromFPMArchive(filepath.Join(outDir, "classic-1.0.0.fpm"))
	require.NoError(t, err)
	assert.False(t, meta.FrontendBuilt)
	assert.Empty(t, meta.FrontendDirs)
}

// TestInstallServesThePackagedSPA is the other half: the compiled frontend has to be
// reachable at /assets/<app>/frontend/ once installed, through the sites/assets/<app>
// symlink frappe's make_asset_dirs creates.
func TestInstallServesThePackagedSPA(t *testing.T) {
	src := crmShapedApp(t, "crm")
	fakeYarn(t, map[string]string{
		"crm/public/frontend/index.html":             spaHTML("crm", "frontend"),
		"crm/public/frontend/assets/index-A1B2C3.js": "console.log(1)",
		"crm/www/crm.html":                           spaHTML("crm", "frontend"),
	})

	args, outDir := packageArgs(t, src, "1.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)
	fpmPath := filepath.Join(outDir, "crm-1.0.0.fpm")

	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir())
	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites", "assets"), 0o755))

	resetInstallCmdFlags()
	out, err = SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench)
	require.NoError(t, err, out)

	// The path the built index.html references must resolve to a real file through the
	// bench's sites/assets link. This is exactly what the browser asks for.
	served := filepath.Join(bench, "sites", "assets", "crm", "frontend", "assets", "index-A1B2C3.js")
	body, readErr := os.ReadFile(served)
	require.NoError(t, readErr, "/assets/crm/frontend/assets/index-A1B2C3.js must be servable")
	assert.Equal(t, "console.log(1)", string(body))

	// The www template frappe renders at /crm must be in the app frappe sees.
	_, err = os.Stat(filepath.Join(bench, "apps", "crm", "crm", "www", "crm.html"))
	assert.NoError(t, err, "frappe's website router needs crm/www/crm.html to route /crm")

	// An SPA-only app has no esbuild bundles, and saying otherwise sends the user
	// after a --bench-path rebuild that cannot produce anything for this app.
	assert.Contains(t, out, "expected for an SPA-only app")
	assert.NotContains(t, out, "Package with 'fpm package --bench-path")
}

// TestInstallWritesTheSPARouteForAPackageMissingIt covers a package produced by older
// tooling: it carries the compiled SPA but no www template, so frappe has no route.
func TestInstallWritesTheSPARouteForAPackageMissingIt(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "gameplan", map[string]string{
		"gameplan/public/frontend/index.html": spaHTML("gameplan", "frontend"),
		"gameplan/www/__init__.py":            "",
		// gameplan really does route at /g, not /gameplan: the name cannot be guessed.
		"gameplan/hooks.py": `app_name = "gameplan"
website_route_rules = [
    {"from_route": "/g/<path:app_path>", "to_route": "g"},
]
`,
	})
	args, outDir := packageArgs(t, src, "1.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir())
	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites", "assets"), 0o755))

	resetInstallCmdFlags()
	out, err = SharedExecuteCommand(rootCmd, "install", filepath.Join(outDir, "gameplan-1.0.0.fpm"), "--bench-path", bench)
	require.NoError(t, err, out)

	body, readErr := os.ReadFile(filepath.Join(bench, "apps", "gameplan", "gameplan", "www", "g.html"))
	require.NoError(t, readErr, "install must supply the missing SPA route template, named from hooks.py")
	assert.Equal(t, spaHTML("gameplan", "frontend"), string(body))
	assert.Contains(t, out, "Wrote the SPA route template")
}

// TestPackageShipsAFrontendNotNamedFrontend is erpnext: its SPA lives in banking/,
// builds into erpnext/public/banking, and routes at erpnext/www/banking.html. Nothing
// in the layout is called "frontend", so packaging must discover the output directory
// rather than assume its name — assuming it would fail erpnext's packaging outright.
func TestPackageShipsAFrontendNotNamedFrontend(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "erpnext", map[string]string{
		"package.json": `{
		  "name": "erpnext",
		  "scripts": {
		    "postinstall": "cd banking && yarn install",
		    "build": "cd banking && yarn build"
		  }
		}`,
		"yarn.lock":                    "# yarn lockfile v1\n",
		"banking/package.json":         `{"name":"banking","scripts":{"build":"vite build --base=/assets/erpnext/banking/"}}`,
		"banking/yarn.lock":            "# yarn lockfile v1\n",
		"erpnext/public/js/erpnext.js": "// hand-maintained, not build output",
		"erpnext/www/support.html":     "<h1>an unrelated www page</h1>",
		"erpnext/www/order.html":       "<h1>a real portal page, not the SPA</h1>",
		// erpnext really does declare two dozen to_routes, most of them DocTypes
		// rather than templates. Only the one that loads the built SPA is a frontend
		// route, and fpm must not treat the others as one.
		"erpnext/hooks.py": `app_name = "erpnext"
website_route_rules = [
	{"from_route": "/orders", "to_route": "Sales Order"},
	{"from_route": "/orders/<path:name>", "to_route": "order"},
	{"from_route": "/banking/<path:app_path>", "to_route": "banking"},
]
`,
	})
	fakeYarn(t, map[string]string{
		"erpnext/public/banking/index.html":        spaHTML("erpnext", "banking"),
		"erpnext/public/banking/assets/app-XYZ.js": "console.log(2)",
		"erpnext/www/banking.html":                 spaHTML("erpnext", "banking"),
	})

	args, outDir := packageArgs(t, src, "15.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	fpmPath := filepath.Join(outDir, "erpnext-15.0.0.fpm")
	meta, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
	require.NoError(t, err)
	assert.True(t, meta.FrontendBuilt)
	assert.Equal(t, []string{"erpnext/public/banking"}, meta.FrontendDirs,
		"the output directory must be discovered, not assumed to be public/frontend")
	assert.Equal(t, []string{"erpnext/www/banking.html"}, meta.FrontendRoutes,
		"only the template that loads the built SPA counts; erpnext's DocType portal routes do not")

	inspectFPM(t, fpmPath, func(filesInArchive map[string]*zip.File, _ *zip.ReadCloser) {
		assert.Contains(t, filesInArchive, "erpnext/public/banking/assets/app-XYZ.js")
		assert.Contains(t, filesInArchive, "erpnext/www/banking.html")
	})
}
