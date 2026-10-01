package update

import "github.com/getbx/bx/internal/releasemanifest"

// The signed release manifest lives in internal/releasemanifest — a leaf with no os/exec, so the
// iPhone's bxkit can verify releases too (the server's deploy fetches the manifest; the phone checks
// the signature). These keep every existing caller compiling unchanged.
type (
	Manifest = releasemanifest.Manifest
	Asset    = releasemanifest.Asset
)

var (
	ParseAndVerify = releasemanifest.ParseAndVerify
	FindAsset      = releasemanifest.FindAsset
	FindPackage    = releasemanifest.FindPackage
)
