package github

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/zx06/opennow/internal/storage/storeerr"
)

// fakeRepo is an in-memory git object store implementing repoAPI with real
// tree semantics (base_tree, nested paths, null-SHA deletes of files and
// directories, fast-forward-only ref updates).
type fakeRepo struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	trees   map[string][]TreeEntry
	commits map[string][2]string // sha → {tree, parent}
	ref     string
	calls   map[string]int
	// beforeUpdate lets tests move the ref concurrently (conflict retry).
	beforeUpdate func()
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{blobs: map[string][]byte{}, trees: map[string][]TreeEntry{}, commits: map[string][2]string{}, calls: map[string]int{}}
}

func (f *fakeRepo) count(op string) { f.calls[op]++ }

func (f *fakeRepo) GetRef(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("GetRef")
	if f.ref == "" {
		return "", ErrEmptyRepo
	}
	return f.ref, nil
}

func (f *fakeRepo) GetCommitTree(ctx context.Context, c string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("GetCommitTree")
	cm, ok := f.commits[c]
	if !ok {
		return "", storeerr.ErrNotFound
	}
	return cm[0], nil
}

func (f *fakeRepo) GetTree(ctx context.Context, sha string) ([]TreeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("GetTree")
	t, ok := f.trees[sha]
	if !ok {
		return nil, storeerr.ErrNotFound
	}
	return append([]TreeEntry(nil), t...), nil
}

func (f *fakeRepo) GetBlob(ctx context.Context, sha string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("GetBlob")
	b, ok := f.blobs[sha]
	if !ok {
		return nil, storeerr.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeRepo) CreateBlob(ctx context.Context, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("CreateBlob")
	sha := GitBlobSHA(data)
	f.blobs[sha] = append([]byte(nil), data...)
	return sha, nil
}

type flatEntry struct{ mode, sha string }

func (f *fakeRepo) flatten(treeSHA, prefix string, out map[string]flatEntry) {
	for _, e := range f.trees[treeSHA] {
		p := prefix + e.Path
		if e.Type == "tree" {
			f.flatten(e.SHA, p+"/", out)
		} else {
			out[p] = flatEntry{e.Mode, e.SHA}
		}
	}
}

func (f *fakeRepo) build(flat map[string]flatEntry, prefix string) string {
	children := map[string]bool{}
	var entries []TreeEntry
	for p, fe := range flat {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			children[rest[:i]] = true
			continue
		}
		entries = append(entries, TreeEntry{Path: rest, Mode: fe.mode, Type: "blob", SHA: fe.sha, Size: int64(len(f.blobs[fe.sha]))})
	}
	for c := range children {
		entries = append(entries, TreeEntry{Path: c, Mode: "040000", Type: "tree", SHA: f.build(flat, prefix+c+"/")})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	h := sha1.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s %s %s\n", e.Mode, e.Path, e.SHA)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	f.trees[sha] = entries
	return sha
}

func (f *fakeRepo) CreateTree(ctx context.Context, base string, changes []Change) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("CreateTree")
	flat := map[string]flatEntry{}
	if base != "" {
		if _, ok := f.trees[base]; !ok {
			return "", fmt.Errorf("base tree %s not found", base)
		}
		f.flatten(base, "", flat)
	}
	for _, c := range changes {
		switch {
		case c.Delete:
			delete(flat, c.Path)
			for p := range flat {
				if strings.HasPrefix(p, c.Path+"/") {
					delete(flat, p)
				}
			}
		case c.Content != nil:
			sha := GitBlobSHA([]byte(*c.Content))
			f.blobs[sha] = []byte(*c.Content)
			flat[c.Path] = flatEntry{c.Mode, sha}
		case c.Type == "tree":
			for p := range flat {
				if strings.HasPrefix(p, c.Path+"/") {
					delete(flat, p)
				}
			}
			f.flatten(c.SHA, c.Path+"/", flat)
		default:
			if _, ok := f.blobs[c.SHA]; !ok {
				return "", fmt.Errorf("422: blob %s does not exist", c.SHA)
			}
			flat[c.Path] = flatEntry{c.Mode, c.SHA}
		}
	}
	return f.build(flat, ""), nil
}

func (f *fakeRepo) CreateCommit(ctx context.Context, msg, tree, parent string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("CreateCommit")
	h := sha1.Sum([]byte(msg + tree + parent + fmt.Sprint(len(f.commits))))
	sha := hex.EncodeToString(h[:])
	f.commits[sha] = [2]string{tree, parent}
	return sha, nil
}

func (f *fakeRepo) UpdateRef(ctx context.Context, commit string) error {
	if f.beforeUpdate != nil {
		hook := f.beforeUpdate
		f.beforeUpdate = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count("UpdateRef")
	if f.commits[commit][1] != f.ref {
		return ErrConflict
	}
	f.ref = commit
	return nil
}

func (f *fakeRepo) InitBranch(ctx context.Context, path string, content []byte, msg string) error {
	s := string(content)
	tree, err := f.CreateTree(ctx, "", []Change{{Path: path, Mode: "100644", Type: "blob", Content: &s}})
	if err != nil {
		return err
	}
	c, _ := f.CreateCommit(ctx, msg, tree, "")
	f.mu.Lock()
	f.ref = c
	f.mu.Unlock()
	return nil
}

func (f *fakeRepo) RateInfo() RateInfo { return RateInfo{Limit: 5000, Remaining: 4999} }
