package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// Getenv returns the value of the environment variable key or def if empty.
// The value is trimmed of leading/trailing whitespace.
func Getenv(key, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}

// GetenvInt returns the value of the environment variable key as an int, or def if empty/invalid.
func GetenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GetenvBool returns the value of the environment variable key as a bool, or def if empty/invalid.
// Recognizes: 1, true, yes, y, on (true) and 0, false, no, n, off (false).
func GetenvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	return def
}

// GetenvDuration returns the value of the environment variable key as a time.Duration, or def if empty/invalid.
func GetenvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// SplitEnvList returns a comma-separated environment variable as a slice of strings.
// Empty entries are skipped, and each entry is trimmed.
func SplitEnvList(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

// LoadDotEnv loads environment variables from a .env-style file if present.
// Existing environment variables are not overwritten.
func LoadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		val = strings.Trim(val, `"'`)
		_ = os.Setenv(key, val)
	}
}
