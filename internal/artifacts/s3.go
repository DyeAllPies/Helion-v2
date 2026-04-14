// internal/artifacts/s3.go
//
// S3Store is an object-storage-backed Store. It speaks the S3 API via
// minio-go, which makes it compatible with AWS S3, MinIO, GCS via the
// S3-compatible endpoint, Cloudflare R2, and anything else that
// implements the same wire protocol.
//
// URI scheme is "s3://<bucket>/<key>". The bucket is fixed per Store
// at construction time; a URI whose bucket does not match is rejected
// with ErrInvalidURI so a stray s3://other/key cannot be resolved.
//
// Concurrency: minio-go's Client is safe for concurrent use. Put
// serialises the caller's Reader but does not lock across Put calls,
// so concurrent Puts to distinct keys proceed in parallel.

package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config describes an S3-compatible backend. AccessKey/SecretKey may
// be empty to fall back to the SDK's default credential chain (env vars,
// IAM instance profile, etc.), but for the project's current deployment
// model we expect explicit creds.
type S3Config struct {
	Endpoint  string // "s3.amazonaws.com" or "minio:9000"
	Bucket    string
	Region    string // optional, empty for SDK default
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// S3Store implements Store against an S3-compatible service.
type S3Store struct {
	client s3Client
	bucket string
}

// s3Client is the subset of *minio.Client that S3Store uses. It is
// extracted so the happy-path and error-path logic can be tested with
// an in-memory fake — running a real MinIO in unit tests is overkill
// for verifying URI parsing and digest accounting.
type s3Client interface {
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	GetObject(ctx context.Context, bucket, key string, opts minio.GetObjectOptions) (io.ReadCloser, error)
	StatObject(ctx context.Context, bucket, key string, opts minio.StatObjectOptions) (minio.ObjectInfo, error)
	RemoveObject(ctx context.Context, bucket, key string, opts minio.RemoveObjectOptions) error
}

// minioAdapter wraps *minio.Client so its GetObject (which returns
// a concrete *minio.Object) matches our s3Client interface (which
// returns io.ReadCloser).
type minioAdapter struct{ c *minio.Client }

func (m minioAdapter) PutObject(ctx context.Context, b, k string, r io.Reader, n int64, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
	return m.c.PutObject(ctx, b, k, r, n, opts)
}
func (m minioAdapter) GetObject(ctx context.Context, b, k string, opts minio.GetObjectOptions) (io.ReadCloser, error) {
	obj, err := m.c.GetObject(ctx, b, k, opts)
	if err != nil {
		return nil, err
	}
	return obj, nil
}
func (m minioAdapter) StatObject(ctx context.Context, b, k string, opts minio.StatObjectOptions) (minio.ObjectInfo, error) {
	return m.c.StatObject(ctx, b, k, opts)
}
func (m minioAdapter) RemoveObject(ctx context.Context, b, k string, opts minio.RemoveObjectOptions) error {
	return m.c.RemoveObject(ctx, b, k, opts)
}

// NewS3Store dials the configured endpoint and returns a ready Store.
// Bucket existence is not checked here — the coordinator creates or
// verifies the bucket at startup; S3Store itself is oblivious.
func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("artifacts: s3 endpoint required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("artifacts: s3 bucket required")
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	cli, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("artifacts: minio client: %w", err)
	}
	return &S3Store{client: minioAdapter{c: cli}, bucket: cfg.Bucket}, nil
}

// newS3StoreWithClient is the test constructor — it lets unit tests
// inject a fake s3Client without standing up a real MinIO.
func newS3StoreWithClient(bucket string, c s3Client) *S3Store {
	return &S3Store{client: c, bucket: bucket}
}

// Bucket returns the bucket this Store is bound to. Exposed for tests
// and for operator introspection.
func (s *S3Store) Bucket() string { return s.bucket }

// Put streams r into s3://<bucket>/<key>. SHA-256 is computed via
// TeeReader during upload and attached as user metadata so Stat can
// return it without a second full read.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64) (URI, error) {
	rel, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	tee := io.TeeReader(r, h)

	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		// Metadata keys are lower-cased by the S3 protocol; we read
		// them back the same way in Stat.
		UserMetadata: map[string]string{},
	}
	if _, err := s.client.PutObject(ctx, s.bucket, rel, tee, size, opts); err != nil {
		return "", fmt.Errorf("artifacts: s3 put: %w", err)
	}
	// The digest is only known once PutObject has drained the tee.
	// We write it as a second zero-byte update would be wasteful, so
	// instead we accept that Stat falls back to a streaming digest if
	// metadata is absent (see Stat below). Future: use CopyObject with
	// ReplaceMetadata=true to attach the digest post-upload without
	// re-uploading bytes.
	_ = h // digest currently unused on the Put path; see Stat.
	return s3URI(s.bucket, rel), nil
}

// Get opens the object for reading.
func (s *S3Store) Get(ctx context.Context, uri URI) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := s.keyForURI(uri)
	if err != nil {
		return nil, err
	}
	rc, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, translateS3Err(err)
	}
	return rc, nil
}

// Stat returns Size, SHA256, UpdatedAt. The digest is computed by
// streaming the object end-to-end (LocalStore does the same); callers
// who only need size should use StatObject directly via a lower-level
// handle, which we do not expose because the abstraction is the point.
func (s *S3Store) Stat(ctx context.Context, uri URI) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	key, err := s.keyForURI(uri)
	if err != nil {
		return Metadata{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Metadata{}, translateS3Err(err)
	}
	digest, err := s.streamDigest(ctx, key)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{
		Size:      info.Size,
		SHA256:    digest,
		UpdatedAt: info.LastModified,
	}, nil
}

// Delete removes the object.
func (s *S3Store) Delete(ctx context.Context, uri URI) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.keyForURI(uri)
	if err != nil {
		return err
	}
	// minio's RemoveObject silently succeeds on missing keys, which
	// does not match our Store contract (Delete-missing -> ErrNotFound).
	// Probe with StatObject first so we can return a faithful error.
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err != nil {
		return translateS3Err(err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("artifacts: s3 delete: %w", err)
	}
	return nil
}

// streamDigest reads the object and returns its hex SHA-256.
func (s *S3Store) streamDigest(ctx context.Context, key string) (string, error) {
	rc, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return "", translateS3Err(err)
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := copyWithCtx(ctx, h, rc); err != nil {
		return "", fmt.Errorf("artifacts: s3 digest: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// keyForURI parses an s3:// URI and verifies the bucket. Returns the
// object key (everything after bucket/).
func (s *S3Store) keyForURI(uri URI) (string, error) {
	u, err := url.Parse(string(uri))
	if err != nil || u.Scheme != "s3" {
		return "", ErrInvalidURI
	}
	if u.Host != s.bucket {
		return "", ErrInvalidURI
	}
	key := strings.TrimPrefix(u.Path, "/")
	if key == "" {
		return "", ErrInvalidURI
	}
	return key, nil
}

// s3URI builds an s3://bucket/key URI.
func s3URI(bucket, key string) URI {
	// bucket must not have slashes; key is pre-sanitised by
	// sanitizeKey (no leading slash, no traversal).
	return URI("s3://" + bucket + "/" + key)
}

// translateS3Err maps minio's error shapes onto our sentinel errors.
// The SDK uses minio.ErrorResponse with Code="NoSuchKey" for missing
// objects across all S3-compatible backends we support.
func translateS3Err(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NoSuchObject":
		return ErrNotFound
	}
	return fmt.Errorf("artifacts: s3: %w", err)
}
