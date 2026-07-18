package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestExtractMacOSPackageReturnsVerifiedPayload(t *testing.T) {
	archive := macOSPackageArchive(t, []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/bx", "cli"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "plist"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Resources/icon", "icon"),
		fileEntry("bx-macos-arm64/README.txt", "readme"),
	})

	payload, err := ExtractMacOSPackage(archive, "arm64")
	if err != nil {
		t.Fatalf("extract package: %v", err)
	}
	if got := string(payload.CLI); got != "cli" {
		t.Fatalf("CLI = %q", got)
	}
	if got := string(payload.Menu["Contents/MacOS/BxMenu"]); got != "menu" {
		t.Fatalf("BxMenu = %q", got)
	}
}

func TestExtractMacOSPackageAcceptsNormalDirectoryEntries(t *testing.T) {
	archive := macOSPackageArchive(t, []macOSArchiveEntry{
		{header: tar.Header{Name: "bx-macos-arm64/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/Contents/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "bx-macos-arm64/Bx.app/Contents/MacOS/", Typeflag: tar.TypeDir, Mode: 0o755}},
		fileEntry("bx-macos-arm64/bx", "cli"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "plist"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
	})

	if _, err := ExtractMacOSPackage(archive, "arm64"); err != nil {
		t.Fatalf("normal directory entries must be accepted: %v", err)
	}
}

func TestExtractMacOSPackageRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{
		"../bx-macos-arm64/bx",
		"/bx-macos-arm64/bx",
		"bx-macos-arm64/../bx",
		`bx-macos-arm64\bx`,
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
	entries = append(entries, fileEntry("bx-macos-arm64/bx", "second cli"))
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
		Name: "bx-macos-arm64/bx",
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

func TestExtractMacOSPackageRequiresCLI(t *testing.T) {
	archive := macOSPackageArchive(t, []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "plist"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
	})
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("package without bx executable was accepted")
	}
}

func TestExtractMacOSPackageRequiresMenu(t *testing.T) {
	archive := macOSPackageArchive(t, []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/bx", "cli"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "plist"),
	})
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("package without BxMenu was accepted")
	}
}

func TestExtractMacOSPackageRequiresInfoPlist(t *testing.T) {
	archive := macOSPackageArchive(t, []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/bx", "cli"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
	})
	if _, err := ExtractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("package without Info.plist was accepted")
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
	return []macOSArchiveEntry{
		fileEntry("bx-macos-arm64/bx", "cli"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/Info.plist", "plist"),
		fileEntry("bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu", "menu"),
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
