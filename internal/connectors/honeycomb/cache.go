package honeycomb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const cacheTTL = 24 * time.Hour
const cacheSchemaVersion = 1

type cacheEnvelope struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Markers   []marker  `json:"markers"`
}

// cacheFingerprint returns a 16-char hex string derived from the token,
// dataset, and base URL. The token is never stored or logged.
func cacheFingerprint(token, dataset, baseURL string) string {
	h := sha256.New()
	h.Write([]byte(token))
	h.Write([]byte{0})
	h.Write([]byte(dataset))
	h.Write([]byte{0})
	h.Write([]byte(baseURL))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// cachePath returns the path for the marker cache file. Returns an error if
// the user cache directory cannot be determined.
func cachePath(fp string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "xray", "honeycomb", fp+".json"), nil
}

// readMarkerCache reads and decodes the cache at path. Returns (markers, true)
// when the cache is present, valid, and within cacheTTL. Any decode or stat
// error returns (nil, false) so callers fall through to a live fetch.
func readMarkerCache(path string) ([]marker, bool) {
	// #nosec G304 -- path derived from os.UserCacheDir() + a sha256 fingerprint; user-controlled cache dir.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var env cacheEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false
	}
	if env.Version != cacheSchemaVersion {
		return nil, false
	}
	if time.Since(env.FetchedAt) > cacheTTL {
		return nil, false
	}
	return env.Markers, true
}

// writeMarkerCache writes markers to path using an atomic temp-file rename.
// Parent directories are created as needed. Errors are logged at debug level
// and never returned; the cache is best-effort.
func writeMarkerCache(path string, markers []marker, log *slog.Logger) {
	env := cacheEnvelope{
		Version:   cacheSchemaVersion,
		FetchedAt: time.Now().UTC(),
		Markers:   markers,
	}
	data, err := json.Marshal(env)
	if err != nil {
		log.Debug("honeycomb: cache marshal failed", "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Debug("honeycomb: cache mkdir failed", "err", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Debug("honeycomb: cache write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Debug("honeycomb: cache rename failed", "err", err)
		_ = os.Remove(tmp)
	}
}
