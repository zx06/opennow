package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultUploadChunkBytes is the recommended client chunk size. Chosen so that
// even at ~20–40 KiB/s through Cloudflare Tunnel each PUT finishes well under
// the ~100s proxy timeout. Clients may use a smaller size; larger is OK if the
// path is fast.
const DefaultUploadChunkBytes = 256 * 1024

const (
	hdrUploadOffset = "X-OpenNow-Offset"
	hdrUploadTotal  = "X-OpenNow-Total"
)

// parseChunkMeta reads optional chunking headers. ok=false means "whole-file PUT".
func parseChunkMeta(r *http.Request) (offset, total int64, ok bool, err error) {
	offS := r.Header.Get(hdrUploadOffset)
	totS := r.Header.Get(hdrUploadTotal)
	if offS == "" && totS == "" {
		// Also accept Content-Range: bytes start-end/total
		if cr := r.Header.Get("Content-Range"); cr != "" {
			return parseContentRange(cr)
		}
		return 0, 0, false, nil
	}
	if offS == "" || totS == "" {
		return 0, 0, false, fmt.Errorf("chunk upload requires both %s and %s", hdrUploadOffset, hdrUploadTotal)
	}
	offset, err = strconv.ParseInt(offS, 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, false, fmt.Errorf("invalid %s", hdrUploadOffset)
	}
	total, err = strconv.ParseInt(totS, 10, 64)
	if err != nil || total < 0 {
		return 0, 0, false, fmt.Errorf("invalid %s", hdrUploadTotal)
	}
	return offset, total, true, nil
}

func parseContentRange(cr string) (offset, total int64, ok bool, err error) {
	// bytes start-end/total
	cr = strings.TrimSpace(cr)
	if !strings.HasPrefix(strings.ToLower(cr), "bytes ") {
		return 0, 0, false, fmt.Errorf("unsupported Content-Range")
	}
	rest := strings.TrimSpace(cr[len("bytes "):])
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("invalid Content-Range")
	}
	total, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || total < 0 {
		return 0, 0, false, fmt.Errorf("invalid Content-Range total")
	}
	se := strings.Split(parts[0], "-")
	if len(se) != 2 {
		return 0, 0, false, fmt.Errorf("invalid Content-Range span")
	}
	offset, err = strconv.ParseInt(se[0], 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, false, fmt.Errorf("invalid Content-Range start")
	}
	return offset, total, true, nil
}

func (h *Handler) partPath(revisionID, rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	if rel == "" || strings.Contains(rel, "..") {
		return "", fmt.Errorf("illegal path")
	}
	dir := filepath.Join(h.cfg.DataDir, "upload-parts", revisionID)
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

func (h *Handler) clearUploadParts(revisionID string) {
	_ = os.RemoveAll(filepath.Join(h.cfg.DataDir, "upload-parts", revisionID))
}

// writeChunk stages body at offset into a part file. When the part reaches
// total bytes it returns complete=true and the absolute path of the assembled file.
func (h *Handler) writeChunk(revisionID, rel string, offset, total int64, body io.Reader, bodyLen int64) (complete bool, partFile string, written int64, err error) {
	partFile, err = h.partPath(revisionID, rel)
	if err != nil {
		return false, "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(partFile), 0o755); err != nil {
		return false, "", 0, err
	}

	var f *os.File
	if offset == 0 {
		f, err = os.OpenFile(partFile, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	} else {
		f, err = os.OpenFile(partFile, os.O_CREATE|os.O_RDWR, 0o644)
	}
	if err != nil {
		return false, "", 0, err
	}
	defer f.Close()

	if offset > 0 {
		st, err := f.Stat()
		if err != nil {
			return false, "", 0, err
		}
		if st.Size() < offset {
			return false, "", 0, fmt.Errorf("chunk offset %d beyond current size %d (upload chunks in order or retry from 0)", offset, st.Size())
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return false, "", 0, err
		}
	}

	var r io.Reader = body
	if bodyLen > 0 {
		r = io.LimitReader(body, bodyLen)
	}
	n, err := io.Copy(f, r)
	if err != nil {
		return false, "", n, err
	}
	// Truncate if we rewrote the last chunk shorter than a previous attempt.
	end := offset + n
	if total > 0 && end == total {
		if err := f.Truncate(total); err != nil {
			return false, "", n, err
		}
	}
	if err := f.Sync(); err != nil {
		return false, "", n, err
	}
	if total > 0 && end >= total {
		return true, partFile, n, nil
	}
	return false, partFile, n, nil
}
