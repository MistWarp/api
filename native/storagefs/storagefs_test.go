package storagefs

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range files {
		target, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.WriteFile(path, zipBytes(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMissingDistinguishesAbsentFiles(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Missing(present) {
		t.Fatal("an existing file is not missing")
	}
	if !Missing(filepath.Join(dir, "absent")) {
		t.Fatal("an absent file is missing")
	}
}

func TestValidateZipArchive(t *testing.T) {
	valid := zipBytes(t, map[string]string{"project.json": "{}", "assets/a.svg": "<svg/>"})
	if !ValidateZipArchive(valid, 1<<20, 1<<20, 10, true) {
		t.Fatal("a normal archive is valid")
	}
	if ValidateZipArchive(zipBytes(t, map[string]string{"../escape": "x"}), 1<<20, 1<<20, 10, true) {
		t.Fatal("path traversal is rejected")
	}
	if ValidateZipArchive(valid, 1<<20, 1<<20, 1, true) {
		t.Fatal("too many entries are rejected")
	}
	if ValidateZipArchive(valid, 1<<20, 3, 10, true) {
		t.Fatal("oversized expanded content is rejected")
	}
	if ValidateZipArchive([]byte("not a zip archive at all"), 1<<20, 1<<20, 10, true) {
		t.Fatal("non-zip data is rejected")
	}
}

func TestMergeZipArchivesPrefersDeltaEntries(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.zip")
	delta := filepath.Join(dir, "delta.zip")
	output := filepath.Join(dir, "merged.zip")
	writeZip(t, base, map[string]string{"a.txt": "base", "b.txt": "kept"})
	writeZip(t, delta, map[string]string{"a.txt": "delta"})
	if !MergeZipArchives(base, delta, output, 1<<20, 10) {
		t.Fatal("merge succeeds")
	}
	reader, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	contents := map[string]string{}
	for _, item := range reader.File {
		source, err := item.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		buffer.ReadFrom(source)
		source.Close()
		contents[item.Name] = buffer.String()
	}
	if contents["a.txt"] != "delta" || contents["b.txt"] != "kept" || len(contents) != 2 {
		t.Fatalf("unexpected merged contents %v", contents)
	}
	if MergeZipArchives(base, delta, output, 1<<20, 1) {
		t.Fatal("merge respects the file limit")
	}
	if !Missing(output) {
		t.Fatal("a failed merge removes its output")
	}
}

func TestConcatenateFilesChecksLength(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "1")
	second := filepath.Join(dir, "2")
	os.WriteFile(first, []byte("abc"), 0o644)
	os.WriteFile(second, []byte("de"), 0o644)
	output := filepath.Join(dir, "out")
	if !ConcatenateFiles(output, []any{first, second}, 5) {
		t.Fatal("concatenation succeeds")
	}
	if data, _ := os.ReadFile(output); string(data) != "abcde" {
		t.Fatalf("unexpected output %q", data)
	}
	if ConcatenateFiles(output, []any{first, second}, 6) || !Missing(output) {
		t.Fatal("a length mismatch fails and removes the output")
	}
	if ConcatenateFiles(output, []any{first, filepath.Join(dir, "absent")}, 3) {
		t.Fatal("a missing chunk fails")
	}
}

func TestPruneCacheDirRemovesExpiredFilesButPreserves(t *testing.T) {
	dir := t.TempDir() + string(os.PathSeparator)
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"stale", "keep", "a.tmp.1"} {
		os.WriteFile(dir+name, []byte("x"), 0o644)
		os.Chtimes(dir+name, old, old)
	}
	os.WriteFile(dir+"fresh", []byte("x"), 0o644)
	PruneCacheDir(dir, "keep")
	for name, want := range map[string]bool{"stale": true, "keep": false, "a.tmp.1": false, "fresh": false} {
		if Missing(dir+name) != want {
			t.Fatalf("%s missing should be %v", name, want)
		}
	}
}
