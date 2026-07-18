package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"
)

const maxMacOSPackageBytes int64 = 128 << 20

type MacOSPayload struct {
	CLI  []byte
	Menu map[string][]byte
}

// ExtractMacOSPackage accepts only the files bx needs to replace the CLI and
// installed menu app. Archive paths are fixed so later installation cannot
// redirect package contents outside those destinations.
func ExtractMacOSPackage(data []byte, arch string) (MacOSPayload, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return MacOSPayload{}, fmt.Errorf("read macOS package gzip: %w", err)
	}
	defer reader.Close()

	root := "bx-macos-" + arch
	appPrefix := root + "/Bx.app/"
	payload := MacOSPayload{Menu: make(map[string][]byte)}
	seen := make(map[string]struct{})
	var total int64
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return MacOSPayload{}, fmt.Errorf("read macOS package tar: %w", err)
		}
		if err := validateMacOSPackagePath(header.Name); err != nil {
			return MacOSPayload{}, err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return MacOSPayload{}, fmt.Errorf("macOS package contains non-regular file %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxMacOSPackageBytes-total {
			return MacOSPayload{}, fmt.Errorf("macOS package is too large")
		}
		total += header.Size
		if header.Name != root+"/bx" && !strings.HasPrefix(header.Name, appPrefix) {
			continue
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return MacOSPayload{}, fmt.Errorf("macOS package has duplicate file %q", header.Name)
		}
		seen[header.Name] = struct{}{}
		content, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil {
			return MacOSPayload{}, fmt.Errorf("read macOS package file %q: %w", header.Name, err)
		}
		if int64(len(content)) != header.Size {
			return MacOSPayload{}, fmt.Errorf("macOS package file %q is truncated", header.Name)
		}
		if header.Name == root+"/bx" {
			payload.CLI = content
			continue
		}
		payload.Menu[strings.TrimPrefix(header.Name, appPrefix)] = content
	}

	if len(payload.CLI) == 0 {
		return MacOSPayload{}, fmt.Errorf("macOS package missing bx executable")
	}
	if len(payload.Menu["Contents/MacOS/BxMenu"]) == 0 {
		return MacOSPayload{}, fmt.Errorf("macOS package missing BxMenu executable")
	}
	if len(payload.Menu["Contents/Info.plist"]) == 0 {
		return MacOSPayload{}, fmt.Errorf("macOS package missing Info.plist")
	}
	return payload, nil
}

func validateMacOSPackagePath(name string) error {
	canonical := strings.TrimSuffix(name, "/")
	if canonical == "" || strings.HasPrefix(canonical, "/") || strings.Contains(canonical, "\\") || path.Clean(canonical) != canonical || strings.HasPrefix(canonical, "../") || strings.Contains(canonical, "/../") {
		return fmt.Errorf("macOS package contains unsafe path %q", name)
	}
	return nil
}
