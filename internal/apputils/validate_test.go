package apputils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeApp(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, f := range []string{"__init__.py", "hooks.py", "modules.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("app_name = \""+name+"\"\n"), 0o644))
	}
}

func TestValidateFrappeApp(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "myapp")
		assert.NoError(t, ValidateFrappeApp(root, "myapp"))
	})

	t.Run("missing module dir is ErrNotFrappeApp", func(t *testing.T) {
		root := t.TempDir()
		err := ValidateFrappeApp(root, "myapp")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotFrappeApp), "should unwrap to ErrNotFrappeApp")
		var nf *NotFrappeAppError
		require.True(t, errors.As(err, &nf))
		assert.Equal(t, "myapp", nf.AppName)
		assert.Contains(t, err.Error(), "app directory")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("each required file", func(t *testing.T) {
		for _, missing := range []string{"__init__.py", "hooks.py", "modules.txt"} {
			root := t.TempDir()
			writeApp(t, root, "myapp")
			require.NoError(t, os.Remove(filepath.Join(root, "myapp", missing)))
			err := ValidateFrappeApp(root, "myapp")
			require.Error(t, err, missing)
			assert.True(t, errors.Is(err, ErrNotFrappeApp))
			assert.Contains(t, err.Error(), missing)
		}
	})

	t.Run("required file is a directory", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "myapp")
		require.NoError(t, os.Remove(filepath.Join(root, "myapp", "hooks.py")))
		require.NoError(t, os.Mkdir(filepath.Join(root, "myapp", "hooks.py"), 0o755))
		err := ValidateFrappeApp(root, "myapp")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a directory, not a file")
	})

	t.Run("empty app name", func(t *testing.T) {
		err := ValidateFrappeApp(t.TempDir(), "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotFrappeApp))
	})

	t.Run("wrapped errors still match", func(t *testing.T) {
		err := ValidateFrappeApp(t.TempDir(), "nope")
		wrapped := errors.Join(errors.New("packaging failed"), err)
		assert.True(t, errors.Is(wrapped, ErrNotFrappeApp))
	})
}

func TestDetectAppModule(t *testing.T) {
	t.Run("single module found without hint", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "custom_app")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		name, err := DetectAppModule(root, "")
		require.NoError(t, err)
		assert.Equal(t, "custom_app", name)
	})

	t.Run("hint wins when valid", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "one")
		writeApp(t, root, "two")
		name, err := DetectAppModule(root, "two")
		require.NoError(t, err)
		assert.Equal(t, "two", name)
	})

	t.Run("ambiguous without hint", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "one")
		writeApp(t, root, "two")
		_, err := DetectAppModule(root, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotFrappeApp))
		assert.Contains(t, err.Error(), "--app-name")
	})

	t.Run("plain python package is not a frappe app", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "__init__.py"), nil, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, "setup.py"), nil, 0o644))
		_, err := DetectAppModule(root, "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotFrappeApp))
	})

	t.Run("bad hint reports the hinted module", func(t *testing.T) {
		root := t.TempDir()
		_, err := DetectAppModule(root, "expected")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected")
	})

	t.Run("hint invalid but a single other module exists", func(t *testing.T) {
		root := t.TempDir()
		writeApp(t, root, "real_app")
		name, err := DetectAppModule(root, "repo-dir-name")
		require.NoError(t, err)
		assert.Equal(t, "real_app", name)
	})
}
