package store

import "path/filepath"

// appDir is the per-user directory that holds the config file, the
// known_hosts file and the secret key.
const appDir = "NLR Shell"

// defaultFont is the terminal font used until the user picks another one.
const defaultFont = "Cascadia Mono"

// defaultDownloadDir returns the directory downloads go to.
func defaultDownloadDir(home string) string { return filepath.Join(home, "Downloads") }
