package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
)

// buildV2Package constructs a valid v2 macOS package (gzip+tar, root
// bx-macos-arm64/Bx.app/...) with the five required files, optionally
// mutated before packing.
func buildV2Package(t *testing.T, mutate func(files map[string][]byte)) []byte {
	t.Helper()
	cli := []byte("cli-bytes")
	bridgeBin := []byte("bridge-bytes")
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	releaseJSON := fmt.Sprintf(`{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"%x","bx-bridge":"%x"}}`, cliSum, bridgeSum)
	files := map[string][]byte{
		"bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu":           []byte("menu"),
		"bx-macos-arm64/Bx.app/Contents/Info.plist":             []byte("<plist/>"),
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-cli":       cli,
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-bridge":    bridgeBin,
		"bx-macos-arm64/Bx.app/Contents/Resources/release.json": []byte(releaseJSON),
	}
	if mutate != nil {
		mutate(files)
	}
	return packMacOSFiles(t, files)
}

// packMacOSFiles builds a gzip+tar archive from files, writing entries in
// sorted key order for determinism.
func packMacOSFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestExtractV2Roundtrip(t *testing.T) {
	archive := buildV2Package(t, nil)

	pkg, err := ExtractMacOSPackage(archive, "arm64")
	if err != nil {
		t.Fatalf("extract package: %v", err)
	}
	if got := string(pkg.CLI); got != "cli-bytes" {
		t.Fatalf("CLI = %q", got)
	}
	if got := string(pkg.Bridge); got != "bridge-bytes" {
		t.Fatalf("Bridge = %q", got)
	}
	if pkg.Release.Version != "1.2.3" {
		t.Fatalf("Release.Version = %q", pkg.Release.Version)
	}
	if got := string(pkg.App["Contents/MacOS/BxMenu"]); got != "menu" {
		t.Fatalf("App[BxMenu] = %q", got)
	}
	if err := pkg.VerifyAssets("darwin", "arm64"); err != nil {
		t.Fatalf("VerifyAssets(darwin,arm64): %v", err)
	}
	if err := pkg.VerifyAssets("darwin", "amd64"); err == nil {
		t.Fatal("VerifyAssets(darwin,amd64) must fail on platform mismatch")
	}
}

func TestExtractV2MissingRequired(t *testing.T) {
	for _, name := range []string{
		"bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu",
		"bx-macos-arm64/Bx.app/Contents/Info.plist",
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-cli",
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-bridge",
		"bx-macos-arm64/Bx.app/Contents/Resources/release.json",
	} {
		t.Run(name, func(t *testing.T) {
			archive := buildV2Package(t, func(files map[string][]byte) {
				delete(files, name)
			})
			if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
				t.Fatalf("package missing %q was accepted", name)
			}
		})
	}
}

func TestExtractV2IgnoresTopLevelBx(t *testing.T) {
	archive := buildV2Package(t, func(files map[string][]byte) {
		files["bx-macos-arm64/bx"] = []byte("legacy cli")
	})

	pkg, err := ExtractMacOSPackage(archive, "arm64")
	if err != nil {
		t.Fatalf("extract package: %v", err)
	}
	if got := string(pkg.CLI); got != "cli-bytes" {
		t.Fatalf("CLI = %q, top-level bx must not leak into it", got)
	}
	for name := range pkg.App {
		if name == "bx" || strings.HasSuffix(name, "/bx") {
			t.Fatalf("App contains ignored top-level bx entry: %q", name)
		}
	}
}

func TestExtractV2BadReleaseJSON(t *testing.T) {
	archive := buildV2Package(t, func(files map[string][]byte) {
		files["bx-macos-arm64/Bx.app/Contents/Resources/release.json"] = []byte("not json")
	})
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("package with invalid release.json was accepted")
	}
}

func TestExtractMacOSPackageAcceptsNormalDirectoryEntries(t *testing.T) {
	archive := buildV2PackageWithDirs(t)
	if _, err := ExtractMacOSPackage(archive, "arm64"); err != nil {
		t.Fatalf("normal directory entries must be accepted: %v", err)
	}
}

func buildV2PackageWithDirs(t *testing.T) []byte {
	t.Helper()
	cli := []byte("cli-bytes")
	bridgeBin := []byte("bridge-bytes")
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	releaseJSON := fmt.Sprintf(`{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"%x","bx-bridge":"%x"}}`, cliSum, bridgeSum)

	entries := []macOSArchiveEntry{
		{header: tar.Header{Name: "bx-macos-arm64/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/Contents/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/Contents/MacOS/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/Contents/Resources/", Typeflag: tar.TypeDir, Mode: 0o755}},
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "<plist/>"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/bx-cli", string(cli)),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/bx-bridge", string(bridgeBin)),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/release.json", releaseJSON),
	}
	return macOSPackageArchive(t, entries)
}

func TestExtractMacOSPackageRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{
		"../bx-macos-arm64/Bx.app/Contents/Resources/extra",
		"/bx-macos-arm64/Bx.app/Contents/Resources/extra",
		"bx-macos-arm64/../Bx.app/Contents/Resources/extra",
		`bx-macos-arm64\Bx.app\Contents\Resources\extra`,
	} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			archive := macOSPackageArchive(t, append(validMacOSPackageEntries(), fileEntry(name, "bad")))
			if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
				t.Fatalf("unsafe path %q was accepted", name)
			}
		})
	}
}

func TestExtractMacOSPackageRejectsSymlink(t *testing.T) {
	entries := validMacOSPackageEntries()
	entries = append(entries, macOSArchiveEntry{header: tar.Header{
		Name:     "bx-macos-arm64/Bx.app/Contents/Resources/link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/tmp/escape",
		Mode:     0o777,
	}})
	archive := macOSPackageArchive(t, entries)
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestExtractMacOSPackageRejectsDuplicateFile(t *testing.T) {
	entries := validMacOSPackageEntries()
	entries = append(entries, fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "second menu"))
	archive := macOSPackageArchive(t, entries)
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("duplicate file was accepted")
	}
}

func TestExtractMacOSPackageRejectsOversizedFile(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "bx-macos-arm64/Bx.app/Contents/Resources/bx-cli",
		Mode: 0o755,
		Size: maxMacOSPackageBytes + 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := ExtractMacOSPackage(out.Bytes(), "arm64"); err == nil {
		t.Fatal("oversized file was accepted")
	}
}

func TestExtractMacOSPackageRejectsOversizedAggregateIncludingIgnoredFiles(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for _, name := range []string{"bx-macos-arm64/ignored-a", "bx-macos-arm64/ignored-b"} {
		size := maxMacOSPackageBytes/2 + 1
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tarWriter, zeroReader{}, size); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range validMacOSPackageEntries() {
		if err := tarWriter.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := ExtractMacOSPackage(out.Bytes(), "arm64"); err == nil {
		t.Fatal("oversized ignored files bypassed aggregate package limit")
	}
}

type macOSArchiveEntry struct {
	header tar.Header
	data   []byte
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func fileEntry(name, data string) macOSArchiveEntry {
	contents := []byte(data)
	return macOSArchiveEntry{
		header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(contents))},
		data:   contents,
	}
}

func validMacOSPackageEntries() []macOSArchiveEntry {
	cli := []byte("cli-bytes")
	bridgeBin := []byte("bridge-bytes")
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	releaseJSON := fmt.Sprintf(`{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"%x","bx-bridge":"%x"}}`, cliSum, bridgeSum)
	return []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "<plist/>"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/bx-cli", string(cli)),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/bx-bridge", string(bridgeBin)),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/release.json", releaseJSON),
	}
}

func macOSPackageArchive(t *testing.T, entries []macOSArchiveEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
