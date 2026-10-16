package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
}

// ObjectStore is the minimal S3 surface the backend needs. It is satisfied by
// the minio-go client wrapper and by MemStore (tests).
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get returns ErrNotFound when the key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// List returns objects under prefix. With delimited=true only direct
	// children are returned and "sub-directories" come back in prefixes.
	List(ctx context.Context, prefix string, delimited bool) (objects []ObjectInfo, prefixes []string, err error)
	DeleteMany(ctx context.Context, keys []string) error
}

// ---------------------------------------------------------------------------
// minio-go implementation

type minioStore struct {
	c      *minio.Client
	bucket string
}

// ClientConfig configures the S3 HTTP client.
type ClientConfig struct {
	Endpoint  string // URL ("https://s3.amazonaws.com", "http://127.0.0.1:9000") or host[:port]
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// PathStyle: "true" forces path-style, "false" forces virtual-hosted
	// style, "" lets the client decide.
	PathStyle string
	// Insecure selects http when Endpoint has no scheme.
	Insecure bool
}

func parseEndpoint(ep string, insecure bool) (host string, secure bool, err error) {
	if ep == "" {
		ep = "s3.amazonaws.com"
	}
	if strings.Contains(ep, "://") {
		u, err := url.Parse(ep)
		if err != nil {
			return "", false, err
		}
		if u.Path != "" && u.Path != "/" {
			return "", false, fmt.Errorf("s3 endpoint must not contain a path: %q", ep)
		}
		return u.Host, u.Scheme == "https", nil
	}
	return strings.TrimRight(ep, "/"), !insecure, nil
}

// NewMinioStore builds an ObjectStore backed by minio-go (pure Go, CGO-free).
func NewMinioStore(cc ClientConfig) (ObjectStore, *minio.Client, error) {
	host, secure, err := parseEndpoint(cc.Endpoint, cc.Insecure)
	if err != nil {
		return nil, nil, err
	}
	lookup := minio.BucketLookupAuto
	switch strings.ToLower(cc.PathStyle) {
	case "1", "true", "yes":
		lookup = minio.BucketLookupPath
	case "0", "false", "no":
		lookup = minio.BucketLookupDNS
	}
	c, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(cc.AccessKey, cc.SecretKey, ""),
		Secure:       secure,
		Region:       cc.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, nil, err
	}
	return &minioStore{c: c, bucket: cc.Bucket}, c, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NotFound" || resp.StatusCode == http.StatusNotFound
}

func (m *minioStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	opts := minio.PutObjectOptions{ContentType: contentType}
	if size < 0 {
		// Unknown length → multipart; keep parts modest to bound memory.
		opts.PartSize = 16 << 20
	}
	_, err := m.c.PutObject(ctx, m.bucket, key, r, size, opts)
	return err
}

func (m *minioStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	obj, err := m.c.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, storeerr.ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	// Stat issues the (single) GET request and surfaces 404s.
	st, err := obj.Stat()
	if err != nil {
		obj.Close()
		if isNotFound(err) {
			return nil, ObjectInfo{}, storeerr.ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	return obj, ObjectInfo{Key: key, Size: st.Size, ContentType: st.ContentType}, nil
}

func (m *minioStore) List(ctx context.Context, prefix string, delimited bool) ([]ObjectInfo, []string, error) {
	var objs []ObjectInfo
	var prefixes []string
	for o := range m.c.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: !delimited}) {
		if o.Err != nil {
			return nil, nil, o.Err
		}
		if delimited && strings.HasSuffix(o.Key, "/") {
			prefixes = append(prefixes, o.Key)
			continue
		}
		objs = append(objs, ObjectInfo{Key: o.Key, Size: o.Size, ContentType: o.ContentType})
	}
	return objs, prefixes, nil
}

func (m *minioStore) DeleteMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	ch := make(chan minio.ObjectInfo, len(keys))
	for _, k := range keys {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	var errs []error
	for e := range m.c.RemoveObjects(ctx, m.bucket, ch, minio.RemoveObjectsOptions{}) {
		if e.Err != nil && !isNotFound(e.Err) {
			errs = append(errs, fmt.Errorf("%s: %w", e.ObjectName, e.Err))
		}
	}
	return errors.Join(errs...)
}

// EnsureBucket creates the bucket when it does not exist.
func EnsureBucket(ctx context.Context, c *minio.Client, bucket, region string) error {
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region})
}

// ---------------------------------------------------------------------------
// Request counting (shown in the admin overview; useful for R2 quotas)

// Counters tracks remote requests by kind.
type Counters struct {
	Put, Get, List, Delete atomic.Int64
}

type countingStore struct {
	ObjectStore
	c *Counters
}

func (s countingStore) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
	s.c.Put.Add(1)
	return s.ObjectStore.Put(ctx, key, r, size, ct)
}
func (s countingStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	s.c.Get.Add(1)
	return s.ObjectStore.Get(ctx, key)
}
func (s countingStore) List(ctx context.Context, prefix string, d bool) ([]ObjectInfo, []string, error) {
	s.c.List.Add(1)
	return s.ObjectStore.List(ctx, prefix, d)
}
func (s countingStore) DeleteMany(ctx context.Context, keys []string) error {
	s.c.Delete.Add(int64((len(keys) + 999) / 1000))
	return s.ObjectStore.DeleteMany(ctx, keys)
}

// ---------------------------------------------------------------------------
// In-memory implementation (tests)

// MemStore is an in-memory ObjectStore used by unit tests.
type MemStore struct {
	mu      sync.Mutex
	objects map[string]memObject
}

type memObject struct {
	data []byte
	ct   string
}

func NewMemStore() *MemStore { return &MemStore{objects: map[string]memObject{}} }

func (m *MemStore) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{data: data, ct: ct}
	return nil
}

func (m *MemStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, ObjectInfo{}, storeerr.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.data)), ObjectInfo{Key: key, Size: int64(len(o.data)), ContentType: o.ct}, nil
}

func (m *MemStore) List(ctx context.Context, prefix string, delimited bool) ([]ObjectInfo, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var objs []ObjectInfo
	seen := map[string]bool{}
	var prefixes []string
	for k, o := range m.objects {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		if delimited {
			if i := strings.Index(rest, "/"); i >= 0 {
				p := prefix + rest[:i+1]
				if !seen[p] {
					seen[p] = true
					prefixes = append(prefixes, p)
				}
				continue
			}
		}
		objs = append(objs, ObjectInfo{Key: k, Size: int64(len(o.data)), ContentType: o.ct})
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	sort.Strings(prefixes)
	return objs, prefixes, nil
}

func (m *MemStore) DeleteMany(ctx context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.objects, k)
	}
	return nil
}

// Keys returns all stored keys (tests).
func (m *MemStore) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
