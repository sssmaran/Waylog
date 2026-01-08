package graph

import (
	"crypto/sha1"
	"encoding/hex"
)

func ID(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
