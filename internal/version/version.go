package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
	// UpdatePublicKey is an Ed25519 public key injected into release builds.
	// Development builds intentionally have no update trust root.
	UpdatePublicKey = "i46a6hQYUX9SeYY/BM2POwz0wkhIuE5wqvdghFEfIP8="
)

func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, Date)
}
