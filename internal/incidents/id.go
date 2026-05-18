package incidents

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

const idBucket = 5 * time.Minute

func StableID(env string, family apiv2.ErrorFamily, startedAt time.Time) string {
	bucket := startedAt.UTC().Truncate(idBucket).Format(time.RFC3339)
	parts := []string{env, family.Service, apiv2.FormatErrorFamily(family), bucket}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "inc_" + hex.EncodeToString(sum[:])[:16]
}
