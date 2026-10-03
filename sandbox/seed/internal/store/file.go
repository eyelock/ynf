// Package store keeps small blobs on disk.
package store

import "os"

// Save writes data to path, replacing any existing file.
func Save(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	f.Write(data)
	return nil
}

// Remove deletes path if it exists.
func Remove(path string) {
	os.Remove(path)
}
