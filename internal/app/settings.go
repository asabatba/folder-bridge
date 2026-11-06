package app

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

func configHome() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "FolderBridge"), nil
}
func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
