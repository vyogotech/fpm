package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Helper to create a temporary directory with files and subdirectories
func createTestDir(t *testing.T, structure map[string]string, baseDir string) {
	t.Helper()
	for path, content := range structure {
		fullPath := filepath.Join(baseDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
		if content != "" || !strings.HasSuffix(path, "/") { // if content is not empty or path is not a directory marker
			if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
				t.Fatalf("Failed to write file %s: %v", fullPath, err)
			}
		} else if strings.HasSuffix(path, "/") { // explicitly create directory if content is empty and path ends with /
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				t.Fatalf("Failed to create directory %s: %v", fullPath, err)
			}
		}
	}
}

func TestCalculateDirectoryChecksum(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		checksum, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		// Expected checksum for an empty directory (depends on exact implementation,
		// specifically how an empty list of paths is handled by the hash)
		// For this implementation, it's hash of "" (empty sorted paths string)
		// Let's calculate it once and use it as expected.
		// If paths is empty, loop is skipped, hash.Sum(nil) on sha256.New()
		// import "crypto/sha256"; import "encoding/hex"
		// h := sha256.New(); hex.EncodeToString(h.Sum(nil))
		expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		if checksum != expected {
			t.Errorf("Expected checksum %s, got %s", expected, checksum)
		}
	})

	t.Run("directory with a few files", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := map[string]string{
			"file1.txt": "hello world",
			"file2.txt": "foo bar",
		}
		createTestDir(t, files, tmpDir)
		checksum1, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		if checksum1 == "" {
			t.Error("Expected checksum to be non-empty")
		}

		// Test idempotency: checksum should be the same for the same content
		checksum2, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		if checksum1 != checksum2 {
			t.Errorf("Expected checksums to be identical for same content, got %s and %s", checksum1, checksum2)
		}
	})

	t.Run("directory with nested files", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := map[string]string{
			"file1.txt":     "hello world",
			"subdir/file2.txt": "foo bar",
			"subdir/file3.txt": "another file",
		}
		createTestDir(t, files, tmpDir)
		checksum, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		if checksum == "" {
			t.Error("Expected checksum to be non-empty")
		}
	})

	t.Run("ignoreFileName works", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := map[string]string{
			"file1.txt":         "hello world",
			"app_metadata.json": `{"name":"test"}`,
			"subdir/file2.txt":  "foo bar",
		}
		createTestDir(t, files, tmpDir)

		checksumWithIgnore, err := CalculateDirectoryChecksum(tmpDir, "app_metadata.json")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		// Create same structure without app_metadata.json to compare checksums
		tmpDir2 := t.TempDir()
		files2 := map[string]string{
			"file1.txt":        "hello world",
			"subdir/file2.txt": "foo bar",
		}
		createTestDir(t, files2, tmpDir2)
		checksumWithoutAppMetadata, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		if checksumWithIgnore != checksumWithoutAppMetadata {
			t.Errorf("Expected checksum with ignore to be %s, got %s", checksumWithoutAppMetadata, checksumWithIgnore)
		}

		// Now calculate checksum *without* ignoring app_metadata.json, it should be different
		checksumWithoutIgnore, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		if checksumWithIgnore == checksumWithoutIgnore {
			t.Error("Expected checksumWithIgnore to be different from checksumWithoutIgnore")
		}
	})

	t.Run("ignoreFileName directory works", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := map[string]string{
			"file1.txt":        "hello world",
			"ignored_dir/file_in_ignored.txt": "content",
			"ignored_dir/sub/another.txt": "more content",
			"subdir/file2.txt": "foo bar",
		}
		// explicitly create ignored_dir as a directory, even if createTestDir might infer it
		createTestDir(t, files, tmpDir)
		if err := os.MkdirAll(filepath.Join(tmpDir, "ignored_dir"), 0755); err != nil {
			t.Fatal(err)
		}


		checksumWithIgnore, err := CalculateDirectoryChecksum(tmpDir, "ignored_dir")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum with ignored_dir failed: %v", err)
		}

		tmpDir2 := t.TempDir()
		files2 := map[string]string{
			"file1.txt":        "hello world",
			"subdir/file2.txt": "foo bar",
		}
		createTestDir(t, files2, tmpDir2)
		checksumExpected, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum for tmpDir2 failed: %v", err)
		}
		if checksumWithIgnore != checksumExpected {
			t.Errorf("Expected checksum with ignore to be %s, got %s", checksumExpected, checksumWithIgnore)
		}
	})


	t.Run("changing file content changes checksum", func(t *testing.T) {
		tmpDir := t.TempDir()
		files1 := map[string]string{"file1.txt": "content1"}
		createTestDir(t, files1, tmpDir)
		checksum1, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		// Recreate the dir with different content for the same file
		// Need to remove old tmpDir content or use a new one. t.TempDir() handles cleanup.
		tmpDir2 := t.TempDir()
		files2 := map[string]string{"file1.txt": "content2"}
		createTestDir(t, files2, tmpDir2)
		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		if checksum1 == checksum2 {
			t.Error("Expected checksums to differ when file content changes")
		}
	})

	t.Run("adding a file changes checksum", func(t *testing.T) {
		tmpDir1 := t.TempDir()
		files1 := map[string]string{"file1.txt": "content"}
		createTestDir(t, files1, tmpDir1)
		checksum1, err := CalculateDirectoryChecksum(tmpDir1, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		tmpDir2 := t.TempDir()
		files2 := map[string]string{
			"file1.txt": "content",
			"file2.txt": "new file",
		}
		createTestDir(t, files2, tmpDir2)
		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		if checksum1 == checksum2 {
			t.Error("Expected checksums to differ when a file is added")
		}
	})

	t.Run("removing a file changes checksum", func(t *testing.T) {
		tmpDir1 := t.TempDir()
		files1 := map[string]string{
			"file1.txt": "content",
			"file2.txt": "to be removed",
		}
		createTestDir(t, files1, tmpDir1)
		checksum1, err := CalculateDirectoryChecksum(tmpDir1, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		tmpDir2 := t.TempDir()
		files2 := map[string]string{"file1.txt": "content"}
		createTestDir(t, files2, tmpDir2)
		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}
		if checksum1 == checksum2 {
			t.Error("Expected checksums to differ when a file is removed")
		}
	})

	t.Run("renaming a file changes checksum", func(t *testing.T) {
		// Because file paths are part of the hash
		tmpDir1 := t.TempDir()
		files1 := map[string]string{"file_orig_name.txt": "content"}
		createTestDir(t, files1, tmpDir1)
		checksum1, err := CalculateDirectoryChecksum(tmpDir1, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		tmpDir2 := t.TempDir()
		files2 := map[string]string{"file_new_name.txt": "content"} // Same content, different name
		createTestDir(t, files2, tmpDir2)
		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed: %v", err)
		}

		if checksum1 == checksum2 {
			t.Error("Expected checksums to differ when a file is renamed")
		}
	})

	t.Run("file vs directory with same name", func(t *testing.T) {
		tmpDirFile := t.TempDir()
		filesFile := map[string]string{"item": "this is a file"}
		createTestDir(t, filesFile, tmpDirFile)
		checksumFile, err := CalculateDirectoryChecksum(tmpDirFile, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum for file case failed: %v", err)
		}

		tmpDirDir := t.TempDir()
		filesDir := map[string]string{"item/": ""} // "item" is a directory
		createTestDir(t, filesDir, tmpDirDir)
		checksumDir, err := CalculateDirectoryChecksum(tmpDirDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum for dir case failed: %v", err)
		}
		if checksumFile == checksumDir {
			t.Error("Expected checksums to differ for a file 'item' vs a directory 'item/'")
		}
	})


	t.Run("symlink handling", func(t *testing.T) {
		tmpDir := t.TempDir()
		files := map[string]string{
			"target.txt": "this is the target",
			"realdir/another.txt": "real file",
		}
		createTestDir(t, files, tmpDir)

		// Create a symlink to a file
		err := os.Symlink(filepath.Join(tmpDir, "target.txt"), filepath.Join(tmpDir, "link_to_file.txt"))
		if err != nil {
			// Skip symlink tests on Windows if not enough perms or not supported well
			// This is a common issue with `go test` on Windows.
			// A more robust check might involve runtime.GOOS == "windows" and checking SeCreateSymbolicLinkPrivilege
			t.Logf("Skipping symlink test: could not create symlink: %v. (This might be due to permissions on Windows)", err)
			t.SkipNow()
			return
		}
		// Create a symlink to a directory
		err = os.Symlink(filepath.Join(tmpDir, "realdir"), filepath.Join(tmpDir, "link_to_dir"))
		if err != nil {
			t.Logf("Skipping symlink test: could not create symlink: %v. (This might be due to permissions on Windows)", err)
			t.SkipNow()
			return
		}


		checksum1, err := CalculateDirectoryChecksum(tmpDir, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed for symlink case: %v", err)
		}
		if checksum1 == "" {
			t.Error("Expected non-empty checksum for symlink case")
		}

		// Create same structure but change symlink target path
		tmpDir2 := t.TempDir()
		files2 := map[string]string{
			"target.txt": "this is the target", // Content is the same
			"target_new.txt": "new target",    // Symlink will point here
			"realdir/another.txt": "real file",
		}
		createTestDir(t, files2, tmpDir2)
		err = os.Symlink(filepath.Join(tmpDir2, "target_new.txt"), filepath.Join(tmpDir2, "link_to_file.txt"))
		if err != nil {
			t.Fatalf("Failed to create symlink in tmpDir2: %v", err)
		}
		err = os.Symlink(filepath.Join(tmpDir2, "realdir"), filepath.Join(tmpDir2, "link_to_dir"))
		if err != nil {
			t.Fatalf("Failed to create dir symlink in tmpDir2: %v", err)
		}


		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed for tmpDir2 symlink case: %v", err)
		}

		if checksum1 == checksum2 {
			t.Error("Expected checksums to differ when symlink target path changes")
		}

		// Test case: symlink target content changes, but link path itself doesn't.
		// Checksum should NOT change if we only hash link target path.
		// If we were hashing content of linked file, it WOULD change.
		// Current implementation hashes link target path, so it should NOT change.
		tmpDir3 := t.TempDir()
		files3 := map[string]string{
			"target.txt": "content has changed here", // Symlink still points to "target.txt" by name
			"realdir/another.txt": "real file",
		}
		createTestDir(t, files3, tmpDir3)
		err = os.Symlink(filepath.Join(tmpDir3, "target.txt"), filepath.Join(tmpDir3, "link_to_file.txt"))
		if err != nil {
			t.Fatalf("Failed to create symlink in tmpDir3: %v", err)
		}
		err = os.Symlink(filepath.Join(tmpDir3, "realdir"), filepath.Join(tmpDir3, "link_to_dir"))
		if err != nil {
			t.Fatalf("Failed to create dir symlink in tmpDir3: %v", err)
		}
		checksum3, err := CalculateDirectoryChecksum(tmpDir3, "")
		if err != nil {
			t.Fatalf("CalculateDirectoryChecksum failed for tmpDir3 symlink case: %v", err)
		}

		// Checksum1 was with "target.txt" content "this is the target"
		// Checksum3 is with "target.txt" content "content has changed here"
		// Since the link "link_to_file.txt" still points to the name "target.txt" (or its equivalent relative path),
		// the symlink's own contribution to the hash (its path + its target path string "target.txt") remains the same.
		// However, the file "target.txt" is ALSO part of the directory structure independently.
		// When its content changes, its independent contribution to the hash changes.
		// Therefore, the overall directory checksum SHOULD change.
		if checksum1 == checksum3 {
			t.Errorf("Expected checksums to DIFFER when the content of a symlink's target file (also in the checksummed dir) changes. Got same checksum %s. Checksum1 %s, Checksum3 %s", checksum1, checksum1, checksum3)
		}

	})

	t.Run("path sorting consistency", func(t *testing.T) {
		// Create files in different orders, checksum should be the same
		tmpDir1 := t.TempDir()
		// Order: b, then a
		files1 := map[string]string{
			"b.txt": "content b",
			"a.txt": "content a",
		}
		createTestDir(t, files1, tmpDir1)
		checksum1, err := CalculateDirectoryChecksum(tmpDir1, "")
		if err != nil {t.Fatalf("Error calc checksum1: %v", err)}

		tmpDir2 := t.TempDir()
		// Order: a, then b
		files2 := map[string]string{
			"a.txt": "content a",
			"b.txt": "content b",
		}
		createTestDir(t, files2, tmpDir2)
		checksum2, err := CalculateDirectoryChecksum(tmpDir2, "")
		if err != nil {t.Fatalf("Error calc checksum2: %v", err)}

		if checksum1 != checksum2 {
			t.Errorf("Checksums differed based on file creation order. Got %s and %s. Paths should be sorted.", checksum1, checksum2)
		}
	})

}
