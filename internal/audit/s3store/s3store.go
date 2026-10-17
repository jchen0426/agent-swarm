// Package s3store keeps audit anchors in an S3 bucket with object lock, the
// write-once store that ADR-016 assumes for profile question 14. The bucket's
// owner, outside the builders, enables object lock with a default retention
// in compliance mode, longer than any record is kept, and takes the anchors
// itself: its job needs the rights to put objects, to list versions, to read
// them and their retention, and to read the bucket's object lock. The
// platform holds no credential for the bucket. It implements the Audit
// application's Anchors port.
package s3store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Config names a bucket and the credentials to reach it.
type Config struct {
	// Endpoint is the store's host, with its port when it is not the
	// scheme's. Insecure allows plain HTTP, for a loopback host only.
	Endpoint string
	Insecure bool
	Region   string
	Bucket   string
	// Prefix is put before every anchor's name.
	Prefix string
}

// Store keeps anchors in one bucket, under one prefix.
type Store struct {
	client *minio.Client
	bucket string
	prefix string
}

// New returns the store cfg names, reached with the access key keyID and
// secretKey, which come from the secret store. It refuses plain HTTP to any
// host but a loopback one.
func New(cfg *Config, keyID, secretKey string) (*Store, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" || keyID == "" || secretKey == "" {
		return nil, errors.New("s3store: an endpoint, a bucket and an access key are required")
	}
	if cfg.Insecure && !loopback(cfg.Endpoint) {
		return nil, fmt.Errorf("s3store: %s is reached over HTTPS; plain HTTP is for a loopback host only", cfg.Endpoint)
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(keyID, secretKey, ""),
		Secure: !cfg.Insecure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3store: %w", err)
	}
	return &Store{client: client, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// loopback reports whether endpoint names a loopback host.
func loopback(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Put writes body under key on condition that no object holds the key, so
// that the owner's job never makes a second version of an anchor. It sends
// the body's MD5, which a bucket with object lock requires.
func (s *Store) Put(ctx context.Context, key string, body []byte) error {
	opts := minio.PutObjectOptions{ContentType: "application/json", SendContentMd5: true}
	opts.SetMatchETagExcept("*")
	_, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, bytes.NewReader(body), int64(len(body)), opts)
	if minio.ToErrorResponse(err).StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %s", app.ErrExists, key)
	}
	if err != nil {
		return fmt.Errorf("s3store: put %s: %w", key, err)
	}
	return nil
}

// List returns every version of every object under the tenant's name,
// delete markers included, without their bodies: a listing without versions
// shows neither a second version of an anchor nor one a delete marker
// hides.
func (s *Store) List(ctx context.Context, tenant string) ([]chain.Stored, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("s3store: %q is not a tenant id", tenant)
	}
	var out []chain.Stored
	for o := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + tenant + "/", Recursive: true, WithVersions: true}) {
		if o.Err != nil {
			return nil, fmt.Errorf("s3store: list %s: %w", tenant, o.Err)
		}
		out = append(out, chain.Stored{Key: strings.TrimPrefix(o.Key, s.prefix), Version: o.VersionID, Deleted: o.IsDeleteMarker})
	}
	return out, nil
}

// Read reads one version of an object, at most one byte past the largest
// anchor, so that a larger one never decodes.
func (s *Store) Read(ctx context.Context, o *chain.Stored) error {
	obj, err := s.client.GetObject(ctx, s.bucket, s.prefix+o.Key, minio.GetObjectOptions{VersionID: o.Version})
	if err != nil {
		return fmt.Errorf("s3store: read %s: %w", o.Key, err)
	}
	defer func() { _ = obj.Close() }()
	body, err := io.ReadAll(io.LimitReader(obj, chain.MaxAnchorBytes+1))
	if err != nil {
		return fmt.Errorf("s3store: read %s: %w", o.Key, err)
	}
	o.Body = body
	return nil
}

// Retention reads when a version's retention in compliance mode ends, zero
// when it has none.
func (s *Store) Retention(ctx context.Context, o *chain.Stored) error {
	until, err := s.retention(ctx, s.prefix+o.Key, o.Version)
	if err != nil {
		return err
	}
	o.LockedUntil = until
	return nil
}

// retention returns when a version's retention in compliance mode ends,
// zero when it has none.
func (s *Store) retention(ctx context.Context, key, version string) (time.Time, error) {
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, key, version)
	if minio.ToErrorResponse(err).Code == "NoSuchObjectLockConfiguration" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("s3store: read the retention of %s: %w", key, err)
	}
	if mode == nil || *mode != minio.Compliance || until == nil {
		return time.Time{}, nil
	}
	return *until, nil
}

// Locked reports whether the bucket keeps objects write-once: object lock is
// enabled, with a default retention in compliance mode, which nobody can
// shorten or lift.
func (s *Store) Locked(ctx context.Context) (locked bool, why string, err error) {
	enabled, mode, validity, _, err := s.client.GetObjectLockConfig(ctx, s.bucket)
	if minio.ToErrorResponse(err).Code == "ObjectLockConfigurationNotFoundError" {
		return false, "the bucket has no object lock", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("s3store: read the bucket's object lock: %w", err)
	}
	switch {
	case enabled != "Enabled":
		return false, "the bucket's object lock is not enabled", nil
	case mode == nil || *mode != minio.Compliance || validity == nil || *validity == 0:
		return false, "the bucket has no default retention in compliance mode", nil
	}
	return true, "", nil
}
