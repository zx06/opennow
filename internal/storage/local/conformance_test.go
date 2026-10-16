package local

import (
	"testing"

	"github.com/zx06/opennow/internal/storage/storagetest"
)

func TestConformance(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, storagetest.Options{LargeFile: 2 << 20, Reopen: func(t *testing.T) storagetest.Backend {
		b2, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		return b2
	}})
}
