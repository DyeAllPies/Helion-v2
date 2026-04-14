// internal/artifacts/config.go
//
// Env-var-driven Store factory. Matches the project convention of
// configuring subsystems via HELION_* environment variables rather than
// YAML. See analytics package for the precedent.

package artifacts

import (
	"fmt"
	"os"
	"strings"
)

// Config captures the artifact-store settings the coordinator reads at
// startup. Fields map 1:1 to env vars so operators can diff their
// deployment against docs without a cross-reference table.
type Config struct {
	Backend   string // "local" (default) or "s3"
	LocalPath string // HELION_ARTIFACTS_PATH

	S3Endpoint  string // HELION_ARTIFACTS_S3_ENDPOINT (host:port, no scheme)
	S3Bucket    string // HELION_ARTIFACTS_S3_BUCKET
	S3Region    string // HELION_ARTIFACTS_S3_REGION
	S3AccessKey string // HELION_ARTIFACTS_S3_ACCESS_KEY
	S3SecretKey string // HELION_ARTIFACTS_S3_SECRET_KEY
	S3UseSSL    bool   // HELION_ARTIFACTS_S3_USE_SSL ("1"/"true"/"yes")
}

// ConfigFromEnv populates a Config from the process environment.
// Unset fields receive their documented defaults.
func ConfigFromEnv() Config {
	return Config{
		Backend:     getenv("HELION_ARTIFACTS_BACKEND", "local"),
		LocalPath:   getenv("HELION_ARTIFACTS_PATH", "./artifacts"),
		S3Endpoint:  os.Getenv("HELION_ARTIFACTS_S3_ENDPOINT"),
		S3Bucket:    os.Getenv("HELION_ARTIFACTS_S3_BUCKET"),
		S3Region:    os.Getenv("HELION_ARTIFACTS_S3_REGION"),
		S3AccessKey: os.Getenv("HELION_ARTIFACTS_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("HELION_ARTIFACTS_S3_SECRET_KEY"),
		S3UseSSL:    truthy(os.Getenv("HELION_ARTIFACTS_S3_USE_SSL")),
	}
}

// Open constructs a Store from the given Config. Unknown backends
// return an error rather than silently falling back to "local" — a
// typo in HELION_ARTIFACTS_BACKEND should be loud.
func Open(c Config) (Store, error) {
	switch c.Backend {
	case "", "local":
		return NewLocalStore(c.LocalPath)
	case "s3":
		return NewS3Store(S3Config{
			Endpoint:  c.S3Endpoint,
			Bucket:    c.S3Bucket,
			Region:    c.S3Region,
			AccessKey: c.S3AccessKey,
			SecretKey: c.S3SecretKey,
			UseSSL:    c.S3UseSSL,
		})
	default:
		return nil, fmt.Errorf("artifacts: unknown backend %q", c.Backend)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
