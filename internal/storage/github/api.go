package github

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zx06/opennow/internal/storage/cache"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

var (
	// ErrEmptyRepo means the branch does not exist yet (or the repo is empty).
	ErrEmptyRepo = errors.New("github: branch has no commits")
	// ErrConflict means the branch moved while we were building a commit.
	ErrConflict = errors.New("github: ref update is not a fast-forward")
	// ErrRateLimited is returned (without calling GitHub) while a rate-limit
	// window is in effect.
	ErrRateLimited = errors.New("github: rate limited")
)

// TreeEntry is an entry of a git tree.
type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size,omitempty"`
}

// Change is one entry in a create-tree request.
type Change struct {
	Path    string
	Mode    string // 100644 (file) or 040000 (tree)
	Type    string // blob or tree
	SHA     string // existing object (mutually exclusive with Content)
	Content *string
	Delete  bool // remove Path (file or whole directory)
}

func (c Change) MarshalJSON() ([]byte, error) {
	m := map[string]any{"path": c.Path, "mode": c.Mode, "type": c.Type}
	switch {
	case c.Delete:
		m["sha"] = nil
	case c.Content != nil:
		m["content"] = *c.Content
	default:
		m["sha"] = c.SHA
	}
	return json.Marshal(m)
}

// RateInfo is the last observed rate-limit state.
type RateInfo struct {
	Limit        int
	Remaining    int
	Reset        time.Time
	BlockedUntil time.Time
	Requests     int64
	NotModified  int64
}

// repoAPI is the subset of the GitHub git-data API the backend uses. The REST
// client implements it; tests use an in-memory fake.
type repoAPI interface {
	GetRef(ctx context.Context) (string, error)
	GetCommitTree(ctx context.Context, commit string) (string, error)
	GetTree(ctx context.Context, sha string) ([]TreeEntry, error)
	GetBlob(ctx context.Context, sha string) (io.ReadCloser, error)
	CreateBlob(ctx context.Context, data []byte) (string, error)
	CreateTree(ctx context.Context, base string, changes []Change) (string, error)
	CreateCommit(ctx context.Context, msg, tree, parent string) (string, error)
	UpdateRef(ctx context.Context, commit string) error
	// InitBranch creates the branch with a single file (works on empty repos).
	InitBranch(ctx context.Context, path string, content []byte, msg string) error
	RateInfo() RateInfo
}

// GitBlobSHA computes the git object id of a blob.
func GitBlobSHA(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// REST client

type restClient struct {
	http   *http.Client
	api    string // https://api.github.com
	owner  string
	repo   string
	branch string
	token  string

	mu   sync.Mutex
	rate RateInfo
	reqs atomic.Int64
	n304 atomic.Int64
	// etags caches GET responses so polling the branch ref is answered with
	// 304 Not Modified, which GitHub does not count against the rate limit.
	etags *cache.LRU[string, etagEntry]
}

type etagEntry struct {
	etag string
	body []byte
}

func newRESTClient(apiURL, owner, repo, branch, token string) *restClient {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &restClient{
		http:   &http.Client{Timeout: 5 * time.Minute},
		api:    strings.TrimRight(apiURL, "/"),
		owner:  owner,
		repo:   repo,
		branch: branch,
		token:  token,
		etags:  cache.New[string, etagEntry](4<<20, 0, func(e etagEntry) int64 { return int64(len(e.body)) + 64 }),
	}
}

func (c *restClient) repoURL(p string) string {
	return fmt.Sprintf("%s/repos/%s/%s/%s", c.api, url.PathEscape(c.owner), url.PathEscape(c.repo), p)
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("github api: %d %s", e.Status, e.Message) }

func (c *restClient) RateInfo() RateInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rate
	r.Requests = c.reqs.Load()
	r.NotModified = c.n304.Load()
	return r
}

func (c *restClient) checkBlocked() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Before(c.rate.BlockedUntil) {
		return fmt.Errorf("%w until %s", ErrRateLimited, c.rate.BlockedUntil.Format(time.RFC3339))
	}
	if c.rate.Limit > 0 && c.rate.Remaining == 0 && now.Before(c.rate.Reset) {
		return fmt.Errorf("%w until %s", ErrRateLimited, c.rate.Reset.Format(time.RFC3339))
	}
	return nil
}

func (c *restClient) observe(resp *http.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := resp.Header
	if v, err := strconv.Atoi(h.Get("X-RateLimit-Limit")); err == nil {
		c.rate.Limit = v
	}
	if v, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil {
		c.rate.Remaining = v
	}
	if v, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		c.rate.Reset = time.Unix(v, 0)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if ra, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
			c.rate.BlockedUntil = time.Now().Add(time.Duration(ra) * time.Second)
		} else if h.Get("X-RateLimit-Remaining") == "0" {
			c.rate.BlockedUntil = c.rate.Reset
		}
	}
}

// do performs a request. body is re-sent on a (single) short secondary
// rate-limit retry.
func (c *restClient) do(ctx context.Context, method, u string, body any, accept string) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		if err := c.checkBlocked(); err != nil {
			// Wait out short secondary-limit windows once instead of failing.
			c.mu.Lock()
			wait := time.Until(c.rate.BlockedUntil)
			c.mu.Unlock()
			if attempt > 0 || wait <= 0 || wait > 60*time.Second {
				return nil, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		if accept == "" {
			accept = "application/vnd.github+json"
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "opennow")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		var cached etagEntry
		var haveCached bool
		if method == http.MethodGet {
			if cached, haveCached = c.etags.Get(u + "|" + accept); haveCached {
				req.Header.Set("If-None-Match", cached.etag)
			}
		}
		c.reqs.Add(1)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		c.observe(resp)
		if resp.StatusCode == http.StatusNotModified && haveCached {
			resp.Body.Close()
			c.n304.Add(1)
			resp.StatusCode = http.StatusOK
			resp.Body = io.NopCloser(bytes.NewReader(cached.body))
			return resp, nil
		}
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0") && attempt == 0 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode >= 300 {
			defer resp.Body.Close()
			var e struct {
				Message string `json:"message"`
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = json.Unmarshal(b, &e)
			if e.Message == "" {
				e.Message = strings.TrimSpace(string(b))
			}
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
				if strings.Contains(strings.ToLower(e.Message), "rate limit") {
					return nil, fmt.Errorf("%w: %s", ErrRateLimited, e.Message)
				}
			}
			return nil, &apiError{Status: resp.StatusCode, Message: e.Message}
		}
		if method == http.MethodGet {
			if et := resp.Header.Get("ETag"); et != "" && !strings.Contains(accept, "raw") {
				const maxCached = 1 << 20
				b, err := io.ReadAll(io.LimitReader(resp.Body, maxCached+1))
				if err != nil {
					resp.Body.Close()
					return nil, err
				}
				if len(b) <= maxCached {
					resp.Body.Close()
					c.etags.Set(u+"|"+accept, etagEntry{etag: et, body: b})
					resp.Body = io.NopCloser(bytes.NewReader(b))
				} else {
					resp.Body = struct {
						io.Reader
						io.Closer
					}{io.MultiReader(bytes.NewReader(b), resp.Body), resp.Body}
				}
			}
		}
		return resp, nil
	}
}

func (c *restClient) doJSON(ctx context.Context, method, u string, body, out any) error {
	resp, err := c.do(ctx, method, u, body, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func (c *restClient) GetRef(ctx context.Context) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.doJSON(ctx, http.MethodGet, c.repoURL("git/ref/heads/"+c.branch), nil, &out)
	if s := statusOf(err); s == http.StatusNotFound || s == http.StatusConflict {
		return "", ErrEmptyRepo
	}
	return out.Object.SHA, err
}

func (c *restClient) GetCommitTree(ctx context.Context, commit string) (string, error) {
	var out struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	err := c.doJSON(ctx, http.MethodGet, c.repoURL("git/commits/"+commit), nil, &out)
	return out.Tree.SHA, err
}

func (c *restClient) GetTree(ctx context.Context, sha string) ([]TreeEntry, error) {
	var out struct {
		Tree      []TreeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	err := c.doJSON(ctx, http.MethodGet, c.repoURL("git/trees/"+sha), nil, &out)
	if statusOf(err) == http.StatusNotFound {
		return nil, storeerr.ErrNotFound
	}
	if err == nil && out.Truncated {
		return nil, fmt.Errorf("github: tree %s truncated (too many entries)", sha)
	}
	return out.Tree, err
}

func (c *restClient) GetBlob(ctx context.Context, sha string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, c.repoURL("git/blobs/"+sha), nil, "application/vnd.github.raw+json")
	if statusOf(err) == http.StatusNotFound {
		return nil, storeerr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *restClient) CreateBlob(ctx context.Context, data []byte) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, c.repoURL("git/blobs"), map[string]string{
		"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64",
	}, &out)
	return out.SHA, err
}

func (c *restClient) CreateTree(ctx context.Context, base string, changes []Change) (string, error) {
	body := map[string]any{"tree": changes}
	if base != "" {
		body["base_tree"] = base
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, c.repoURL("git/trees"), body, &out)
	return out.SHA, err
}

func (c *restClient) CreateCommit(ctx context.Context, msg, tree, parent string) (string, error) {
	body := map[string]any{"message": msg, "tree": tree, "parents": []string{}}
	if parent != "" {
		body["parents"] = []string{parent}
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, c.repoURL("git/commits"), body, &out)
	return out.SHA, err
}

func (c *restClient) UpdateRef(ctx context.Context, commit string) error {
	err := c.doJSON(ctx, http.MethodPatch, c.repoURL("git/refs/heads/"+c.branch), map[string]any{"sha": commit, "force": false}, nil)
	if s := statusOf(err); s == http.StatusUnprocessableEntity || s == http.StatusConflict {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func (c *restClient) InitBranch(ctx context.Context, path string, content []byte, msg string) error {
	// The contents API works on empty repositories, unlike the git-data API.
	err := c.doJSON(ctx, http.MethodPut, c.repoURL("contents/"+path), map[string]any{
		"message": msg, "content": base64.StdEncoding.EncodeToString(content), "branch": c.branch,
	}, nil)
	if statusOf(err) == http.StatusNotFound || statusOf(err) == http.StatusUnprocessableEntity {
		// Non-empty repo but missing branch: create an orphan commit + ref.
		s := string(content)
		tree, terr := c.CreateTree(ctx, "", []Change{{Path: path, Mode: "100644", Type: "blob", Content: &s}})
		if terr != nil {
			return fmt.Errorf("%v (create branch: %w)", err, terr)
		}
		commit, cerr := c.CreateCommit(ctx, msg, tree, "")
		if cerr != nil {
			return cerr
		}
		return c.doJSON(ctx, http.MethodPost, c.repoURL("git/refs"), map[string]string{"ref": "refs/heads/" + c.branch, "sha": commit}, nil)
	}
	return err
}
