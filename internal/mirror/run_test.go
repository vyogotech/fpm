package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoBuildFrontendAssets(t *testing.T) {
	checkout := t.TempDir()
	appModule := filepath.Join(checkout, "testapp")
	requireNoError := func(err error) {
		if err != nil {
			t.Helper()
			t.Fatal(err)
		}
	}

	requireNoError(os.MkdirAll(appModule, 0o755))
	frontendDir := filepath.Join(checkout, "frontend")
	requireNoError(os.MkdirAll(frontendDir, 0o755))

	// Create package.json with a build script that writes mock bundles
	mockBuildScript := "mkdir -p dist/js dist/css && echo 'console.log(1)' > dist/js/testapp.bundle.12345678.js && echo 'body{}' > dist/css/testapp.bundle.87654321.css"
	pkgJSON := `{"name": "testapp-frontend", "scripts": {"build": "` + mockBuildScript + `"}}`
	requireNoError(os.WriteFile(filepath.Join(frontendDir, "package.json"), []byte(pkgJSON), 0o644))

	ws, err := NewWorkspace(t.TempDir(), false)
	requireNoError(err)

	runner := &Runner{
		Workspace: ws,
		Log:       func(format string, args ...any) {},
	}

	buildRoot, cleanup, err := runner.autoBuildFrontendAssets("testapp", checkout)
	t.Cleanup(cleanup)
	requireNoError(err)
	if buildRoot != checkout {
		t.Fatalf("buildRoot = %q, want the checkout %q", buildRoot, checkout)
	}

	// A build that wrote beside its own project is adopted into the app module, where
	// frappe can serve it. The destination is public/frontend, not public/dist: dist is
	// where frappe's esbuild puts the hashed *.bundle.* files that go into assets.json,
	// and this output is not that.
	destJS := filepath.Join(appModule, "public", "frontend", "js", "testapp.bundle.12345678.js")
	if _, err := os.Stat(destJS); os.IsNotExist(err) {
		t.Fatalf("expected built asset at %s, but file was not found", destJS)
	}

	destCSS := filepath.Join(appModule, "public", "frontend", "css", "testapp.bundle.87654321.css")
	if _, err := os.Stat(destCSS); os.IsNotExist(err) {
		t.Fatalf("expected built asset at %s, but file was not found", destCSS)
	}
}
