package bxkit

import (
	"encoding/json"
	"errors"

	"github.com/getbx/bx/internal/mobileconfig"
	"github.com/getbx/bx/internal/policysync"
)

// SyncURL is where this phone reads the rules its Mac pushed: the reserved sync name (routed
// into the tunnel and onto the VPS's loopback store by the phone's own config) and the blob id
// derived from the link — the same id the Mac derives from the same link.
func SyncURL(link string) (string, error) {
	keys, err := policysync.Derive(link)
	if err != nil {
		return "", err
	}
	return "http://" + mobileconfig.SyncHost + "/v1/blob/" + keys.BlobID, nil
}

// OpenSynced opens a blob fetched from SyncURL and returns the policy as JSON
// ({"version","updated_at","global","direct","proxy"}). A blob sealed with another link says
// so ("different link"), so the app can tell the person instead of guessing.
func OpenSynced(link string, blob []byte) (string, error) {
	keys, err := policysync.Derive(link)
	if err != nil {
		return "", err
	}
	p, err := policysync.Open(keys, blob)
	if errors.Is(err, policysync.ErrWrongKey) {
		return "", errors.New("these rules were synced from a different link, or were altered; ignored")
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	return string(b), err
}
