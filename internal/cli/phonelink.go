package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/linkkind"
)

// phoneLinkCommand prints the link the iPhone app needs for the server this Mac uses. Hidden: its
// caller is the menu's "Add to iPhone…", which runs it through an administrator prompt (the config is
// root-only) and shows the result as a QR for the phone's camera — the link itself is never printed
// to a screen as text.
func phoneLinkCommand() *cli.Command {
	return &cli.Command{
		Name:   "phone-link",
		Usage:  "print the current server's link for the iPhone app (used by the menu's Add to iPhone)",
		Hidden: true,
		Action: func(*cli.Context) error {
			raw, err := os.ReadFile(defaultConfigPath)
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("reading %s needs administrator rights", defaultConfigPath)
			}
			if err != nil {
				return err
			}
			link, err := phoneLinkFromConfig(raw)
			if err != nil {
				return err
			}
			fmt.Println(link)
			return nil
		},
	}
}

// phoneLinkFromConfig is the current server's main link, as bx://. The iPhone runs reality only, so
// any other kind is refused in words that say so.
func phoneLinkFromConfig(raw []byte) (string, error) {
	cfg, err := config.Parse(raw)
	if err != nil {
		return "", err
	}
	inner := cfg.Server
	if decoded, err := blink.Decode(cfg.Server); err == nil {
		inner = decoded
	}
	if linkkind.Kind(inner) != linkkind.KindReality {
		return "", fmt.Errorf("the server this Mac uses is a %s server; bx on iPhone needs a reality server", linkkind.Kind(inner))
	}
	return blink.Encode(inner), nil
}
