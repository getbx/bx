package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/getbx/bx/internal/release"
)

const maxMacOSPackageBytes int64 = 128 << 20

// MacOSPayload is the flattened shape PrepareMacOSInstall consumes: the CLI
// binary to install and the Bx.app tree (relative path -> content) to stage.
type MacOSPayload struct {
	CLI  []byte
	Menu map[string][]byte
}

// MacOSPackage is the parsed contents of a v2 macOS release package, which
// contains only Bx.app/**: the CLI runtime and the /usr/local/bin bridge live
// inside Contents/Resources alongside a self-describing release.json, and App
// holds every regular file under Bx.app keyed by its path relative to it.
type MacOSPackage struct {
	CLI     []byte
	Bridge  []byte
	Release release.Info
	App     map[string][]byte
}

// requiredMacOSAppFiles are the Bx.app-relative paths a v2 package must
// contain; anything else under Bx.app/ is accepted but optional.
var requiredMacOSAppFiles = []string{
	"Contents/MacOS/BxMenu",
	"Contents/Info.plist",
	"Contents/Resources/bx-cli",
	"Contents/Resources/bx-bridge",
	"Contents/Resources/release.json",
}

// ExtractMacOSPackage accepts only the files under bx-macos-<arch>/Bx.app/;
// any other archive entries (including the legacy top-level bx executable)
// are ignored. Archive paths are fixed so later installation cannot redirect
// package contents outside those destinations.
func ExtractMacOSPackage(data []byte, arch string) (MacOSPackage, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return MacOSPackage{}, fmt.Errorf("read macOS package gzip: %w", err)
	}
	defer reader.Close()

	root := "bx-macos-" + arch
	appPrefix := root + "/Bx.app/"
	app := make(map[string][]byte)
	seen := make(map[string]struct{})
	var total int64
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return MacOSPackage{}, fmt.Errorf("read macOS package tar: %w", err)
		}
		if err := validateMacOSPackagePath(header.Name); err != nil {
			return MacOSPackage{}, err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return MacOSPackage{}, fmt.Errorf("macOS package contains non-regular file %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxMacOSPackageBytes-total {
			return MacOSPackage{}, fmt.Errorf("macOS package is too large")
		}
		total += header.Size
		if !strings.HasPrefix(header.Name, appPrefix) {
			continue
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return MacOSPackage{}, fmt.Errorf("macOS package has duplicate file %q", header.Name)
		}
		seen[header.Name] = struct{}{}
		content, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil {
			return MacOSPackage{}, fmt.Errorf("read macOS package file %q: %w", header.Name, err)
		}
		if int64(len(content)) != header.Size {
			return MacOSPackage{}, fmt.Errorf("macOS package file %q is truncated", header.Name)
		}
		app[strings.TrimPrefix(header.Name, appPrefix)] = content
	}

	for _, name := range requiredMacOSAppFiles {
		if len(app[name]) == 0 {
			return MacOSPackage{}, fmt.Errorf("macOS package missing required file %q", name)
		}
	}
	info, err := release.Parse(app["Contents/Resources/release.json"])
	if err != nil {
		return MacOSPackage{}, fmt.Errorf("parse macOS release metadata: %w", err)
	}
	return MacOSPackage{
		CLI:     app["Contents/Resources/bx-cli"],
		Bridge:  app["Contents/Resources/bx-bridge"],
		Release: info,
		App:     app,
	}, nil
}

// VerifyAssets checks the release metadata targets goos/goarch and that the
// extracted CLI and bridge binaries match their recorded digests.
func (p MacOSPackage) VerifyAssets(goos, goarch string) error {
	if err := p.Release.MatchesPlatform(goos, goarch); err != nil {
		return err
	}
	if err := p.Release.VerifyAsset("bx-cli", p.CLI); err != nil {
		return err
	}
	if err := p.Release.VerifyAsset("bx-bridge", p.Bridge); err != nil {
		return err
	}
	return nil
}

func validateMacOSPackagePath(name string) error {
	canonical := strings.TrimSuffix(name, "/")
	if canonical == "" || strings.HasPrefix(canonical, "/") || strings.Contains(canonical, "\\") || path.Clean(canonical) != canonical || strings.HasPrefix(canonical, "../") || strings.Contains(canonical, "/../") {
		return fmt.Errorf("macOS package contains unsafe path %q", name)
	}
	return nil
}
