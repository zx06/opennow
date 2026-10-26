package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client talks to the opennow admin API (same as the web admin).
type Client struct {
	BaseURL string
	APIKey  string
	client  *http.Client
	cookies []*http.Cookie
	// ChunkSize overrides the server-advertised chunk size (0 = use server / 256KiB default).
	ChunkSize int
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) login() error {
	body, _ := json.Marshal(map[string]string{"api_key": c.APIKey})
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login failed: %s", strings.TrimSpace(string(b)))
	}
	c.cookies = resp.Cookies()
	return nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	return c.client.Do(req)
}

// Publish uploads a file or directory tree using chunked PUTs when needed.
func (c *Client) Publish(slug, source string) (map[string]any, error) {
	if err := c.login(); err != nil {
		return nil, err
	}
	createBody, _ := json.Marshal(map[string]string{"slug": slug})
	req, _ := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/publish", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var created struct {
		RevisionID string `json:"revision_id"`
		Slug       string `json:"slug"`
		ChunkSize  int    `json:"chunk_size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("create publish failed: status %d", resp.StatusCode)
	}
	chunk := c.ChunkSize
	if chunk <= 0 {
		chunk = created.ChunkSize
	}
	if chunk <= 0 {
		chunk = 256 * 1024
	}

	st, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		err = filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			return c.uploadFileChunked(created.RevisionID, rel, path, chunk)
		})
	} else {
		err = c.uploadFileChunked(created.RevisionID, filepath.Base(source), source, chunk)
	}
	if err != nil {
		return nil, err
	}
	fin, _ := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/publish/"+created.RevisionID+"/finalize", nil)
	resp2, err := c.do(fin)
	if err != nil {
		return nil, fmt.Errorf("finalize request: %w", err)
	}
	defer resp2.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp2.StatusCode != http.StatusOK {
		return out, fmt.Errorf("finalize failed (%d): %v", resp2.StatusCode, out["error"])
	}
	return out, nil
}

func (c *Client) uploadFileChunked(revisionID, relPath, filePath string, chunkSize int) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	total := st.Size()
	url := c.BaseURL + "/api/v1/publish/" + revisionID + "/files/" + filepath.ToSlash(relPath)

	if total == 0 {
		return c.putChunk(url, bytes.NewReader(nil), 0, 0, 0)
	}

	buf := make([]byte, chunkSize)
	var offset int64
	for offset < total {
		n, err := io.ReadFull(f, buf)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			// last partial chunk
		} else if err != nil {
			return err
		}
		if n == 0 {
			break
		}
		if err := c.putChunk(url, bytes.NewReader(buf[:n]), int64(n), offset, total); err != nil {
			return fmt.Errorf("upload %s @%d: %w", relPath, offset, err)
		}
		offset += int64(n)
		if offset >= total {
			break
		}
	}
	return nil
}

func (c *Client) putChunk(url string, body io.Reader, size, offset, total int64) error {
	req, err := http.NewRequest(http.MethodPut, url, body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-OpenNow-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("X-OpenNow-Total", strconv.FormatInt(total, 10))
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("network/timeout: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (c *Client) ListSites() ([]map[string]any, error) {
	if err := c.login(); err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodGet, c.BaseURL+"/api/v1/sites", nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Sites []map[string]any `json:"sites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Sites, nil
}

func (c *Client) DeleteSite(slug string) error {
	if err := c.login(); err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodDelete, c.BaseURL+"/api/v1/sites/"+slug, nil)
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete failed: %s", b)
	}
	return nil
}

func (c *Client) Rollback(slug, versionID string) error {
	if err := c.login(); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"version_id": versionID})
	req, _ := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/sites/"+slug+"/rollback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rollback failed: %s", b)
	}
	return nil
}

func (c *Client) RenameSite(oldSlug, newSlug string) (map[string]any, error) {
	if err := c.login(); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"slug": newSlug})
	req, _ := http.NewRequest(http.MethodPatch, c.BaseURL+"/api/v1/sites/"+oldSlug, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rename failed: %s", b)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
