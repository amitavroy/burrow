package sync

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
)

// fileMD5 returns the hex MD5 of the file at path, streamed so the file is
// never held in memory.
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
