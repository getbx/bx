package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacOSPackageArtifactsStayOutOfApplicationSearch(t *testing.T) {
	for _, script := range []string{"package-macos-menu.sh", "package-macos-release.sh"} {
		source, err := os.ReadFile(filepath.Join("..", "..", "scripts", script))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(source), ".metadata_never_index") {
			t.Fatalf("%s does not keep build artifacts out of Spotlight application search", script)
		}
	}
}
