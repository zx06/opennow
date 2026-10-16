package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Client talks to the opennow admin API (same as the web admin).
type Client struct {
	BaseURL string
	APIKey  string
	client  *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		client:  &http.Client{},
	}
}

// do sends req authenticated with the API key as a bearer token (no
// session cookie, so no CSRF header is needed).
func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	return c.client.Do(req)
}

// Publish uploads a file or directory tree.
func (c *Client) Publish(slug, source string) (map[string]any, error) {
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, err
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
			return c.uploadFile(created.RevisionID, rel, path)
		})
	} else {
		err = c.uploadFile(created.RevisionID, filepath.Base(source), source)
	}
	if err != nil {
		return nil, err
	}
	fin, _ := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1/publish/"+created.RevisionID+"/finalize", nil)
	resp2, err := c.do(fin)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp2.StatusCode != http.StatusOK {
		return out, fmt.Errorf("finalize failed: %v", out)
	}
	return out, nil
}

func (c *Client) uploadFile(revisionID, relPath, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	url := c.BaseURL + "/api/v1/publish/" + revisionID + "/files/" + filepath.ToSlash(relPath)
	req, err := http.NewRequest(http.MethodPut, url, f)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload %s: %s", relPath, b)
	}
	return nil
}

func (c *Client) ListSites() ([]map[string]any, error) {
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
