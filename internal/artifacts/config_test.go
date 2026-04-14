package artifacts

import (
	"path/filepath"
	"testing"
)

func TestOpen_LocalDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	s, err := Open(Config{Backend: "local", LocalPath: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Fatalf("expected *LocalStore, got %T", s)
	}
}

func TestOpen_EmptyBackendIsLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	s, err := Open(Config{LocalPath: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := s.(*LocalStore); !ok {
		t.Fatalf("expected *LocalStore, got %T", s)
	}
}

func TestOpen_S3RequiresEndpointAndBucket(t *testing.T) {
	if _, err := Open(Config{Backend: "s3"}); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if _, err := Open(Config{Backend: "s3", S3Endpoint: "minio:9000"}); err == nil {
		t.Fatal("expected error for missing bucket")
	}
}

func TestOpen_S3ReturnsStore(t *testing.T) {
	s, err := Open(Config{
		Backend:     "s3",
		S3Endpoint:  "minio:9000",
		S3Bucket:    "helion",
		S3AccessKey: "k",
		S3SecretKey: "v",
	})
	if err != nil {
		t.Fatalf("Open s3: %v", err)
	}
	if _, ok := s.(*S3Store); !ok {
		t.Fatalf("expected *S3Store, got %T", s)
	}
}

func TestOpen_UnknownBackend(t *testing.T) {
	if _, err := Open(Config{Backend: "gcs"}); err == nil {
		t.Fatal("expected unknown-backend error")
	}
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("HELION_ARTIFACTS_BACKEND", "")
	t.Setenv("HELION_ARTIFACTS_PATH", "")
	c := ConfigFromEnv()
	if c.Backend != "local" {
		t.Fatalf("Backend default: %q", c.Backend)
	}
	if c.LocalPath == "" {
		t.Fatal("LocalPath default is empty")
	}
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("HELION_ARTIFACTS_BACKEND", "s3")
	t.Setenv("HELION_ARTIFACTS_PATH", "/var/lib/h")
	t.Setenv("HELION_ARTIFACTS_S3_ENDPOINT", "minio:9000")
	t.Setenv("HELION_ARTIFACTS_S3_BUCKET", "helion")
	t.Setenv("HELION_ARTIFACTS_S3_REGION", "us-east-1")
	t.Setenv("HELION_ARTIFACTS_S3_ACCESS_KEY", "ak")
	t.Setenv("HELION_ARTIFACTS_S3_SECRET_KEY", "sk")
	t.Setenv("HELION_ARTIFACTS_S3_USE_SSL", "true")
	c := ConfigFromEnv()
	if c.Backend != "s3" || c.LocalPath != "/var/lib/h" ||
		c.S3Endpoint != "minio:9000" || c.S3Bucket != "helion" ||
		c.S3Region != "us-east-1" || c.S3AccessKey != "ak" ||
		c.S3SecretKey != "sk" || !c.S3UseSSL {
		t.Fatalf("ConfigFromEnv: %+v", c)
	}
}

func TestTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		if !truthy(v) {
			t.Errorf("expected truthy(%q)", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "nope"} {
		if truthy(v) {
			t.Errorf("expected !truthy(%q)", v)
		}
	}
}
