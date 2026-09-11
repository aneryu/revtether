//go:build unix

package relay

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	lockName  = "relay.lock"
	sockName  = "hub.sock"
	ownerName = "owner.json"
	kindCLI   = "cli"
	kindApp   = "app"
)

func SupportDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "ReverseTether"), nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "revtether"), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("revtether-%d", os.Getuid())), nil
}

func ownerLabel(kind string) string {
	if kind == kindApp {
		return "Reverse Tether.app"
	}
	return "revtether"
}
