package apputils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAppNameFromHooks(t *testing.T) {
	createHooksFile := func(t *testing.T, dir, content string) string {
		t.Helper()
		hooksPath := filepath.Join(dir, "hooks.py") // Standard name for the hooks file
		require.NoError(t, os.WriteFile(hooksPath, []byte(content), 0644))
		return hooksPath
	}

	t.Run("valid app_name double quotes", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `app_name = "test_app_double"`)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "test_app_double", appName)
	})

	t.Run("valid app_name single quotes", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `app_name = 'test_app_single'`)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "test_app_single", appName)
	})

	t.Run("app_name with extra spaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `  app_name   =   "spaced_app"  `)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "spaced_app", appName)
	})

	t.Run("app_name with trailing comment", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `app_name = "commented_app_val" # This is the app name`)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "commented_app_val", appName)
	})

	t.Run("app_name commented out", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `# app_name = "commented_app"`)
		_, err := GetAppNameFromHooks(hooksPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app_name not found or pattern not matched")
	})

	t.Run("no app_name variable", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `
other_variable = "some_value"
# No app_name here
		`)
		_, err := GetAppNameFromHooks(hooksPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app_name not found or pattern not matched")
	})

	t.Run("file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		nonExistentPath := filepath.Join(tmpDir, "non_existent_hooks.py")
		_, err := GetAppNameFromHooks(nonExistentPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "hooks file not found")
	})

	t.Run("app_name in a different format (dict)", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `my_hooks_dict = {"app_name": "dict_app"}`)
		_, err := GetAppNameFromHooks(hooksPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app_name not found or pattern not matched")
	})

	t.Run("multiple app_name assignments", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `
app_name = "first_app"
# Some other code
app_name = "second_app"
		`)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "first_app", appName, "Should return the first assignment found")
	})

	t.Run("app_name is empty string", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `app_name = ""`)
		appName, err := GetAppNameFromHooks(hooksPath)
		assert.NoError(t, err)
		assert.Equal(t, "", appName)
	})

	t.Run("line with only app_name and no assignment", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooksPath := createHooksFile(t, tmpDir, `app_name`)
		_, err := GetAppNameFromHooks(hooksPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "app_name not found or pattern not matched")
	})
}

func TestGetAppMetadataFromHooks(t *testing.T) {
	createHooksFile := func(t *testing.T, dir, content string) string {
		t.Helper()
		hooksPath := filepath.Join(dir, "hooks.py")
		require.NoError(t, os.WriteFile(hooksPath, []byte(content), 0644))
		return hooksPath
	}

	t.Run("extracts all standard metadata fields", func(t *testing.T) {
		tmpDir := t.TempDir()
		content := `
app_name = "crm"
app_title = "Frappe CRM"
app_publisher = "Frappe Technologies Pvt. Ltd."
app_description = "Modern CRM built on Frappe"
app_icon = "octicon octicon-star"
app_color = "grey"
app_email = "developers@frappe.io"
app_license = "GNU General Public License (v3)"
`
		hooksPath := createHooksFile(t, tmpDir, content)
		meta, err := GetAppMetadataFromHooks(hooksPath)
		require.NoError(t, err)
		assert.Equal(t, "crm", meta.AppName)
		assert.Equal(t, "Frappe CRM", meta.AppTitle)
		assert.Equal(t, "Frappe Technologies Pvt. Ltd.", meta.AppPublisher)
		assert.Equal(t, "Modern CRM built on Frappe", meta.AppDescription)
		assert.Equal(t, "octicon octicon-star", meta.AppIcon)
		assert.Equal(t, "grey", meta.AppColor)
		assert.Equal(t, "developers@frappe.io", meta.AppEmail)
		assert.Equal(t, "GNU General Public License (v3)", meta.AppLicense)
	})

	t.Run("handles single quotes and triple quotes", func(t *testing.T) {
		tmpDir := t.TempDir()
		content := `
app_name = 'helpdesk'
app_title = 'Helpdesk'
app_description = """Helpdesk & Customer Support
Multi-line description"""
app_publisher = '''Frappe'''
app_license = 'MIT'
app_icon = "fa fa-ticket"
`
		hooksPath := createHooksFile(t, tmpDir, content)
		meta, err := GetAppMetadataFromHooks(hooksPath)
		require.NoError(t, err)
		assert.Equal(t, "helpdesk", meta.AppName)
		assert.Equal(t, "Helpdesk", meta.AppTitle)
		assert.Equal(t, "Frappe", meta.AppPublisher)
		assert.Equal(t, "MIT", meta.AppLicense)
		assert.Equal(t, "fa fa-ticket", meta.AppIcon)
		assert.Contains(t, meta.AppDescription, "Helpdesk & Customer Support")
	})
}
