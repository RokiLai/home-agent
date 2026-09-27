package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

func loadOrCreateHardwareFingerprintKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "hardware-fingerprint.key")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("hardware fingerprint key must be 32 bytes")
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0600 {
			return nil, fmt.Errorf("hardware fingerprint key permissions must be 0600")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}
