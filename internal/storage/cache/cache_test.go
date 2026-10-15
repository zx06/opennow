package cache

import (
	"testing"
	"time"
)

func TestLRUEvictionAndTTL(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, []byte](10, time.Minute, func(b []byte) int64 { return int64(len(b)) })
	c.SetClock(func() time.Time { return now })
	c.Set("a", make([]byte, 4))
	c.Set("b", make([]byte, 4))
	if _, ok := c.Get("a"); !ok { // a is now most recent
		t.Fatal("a missing")
	}
	c.Set("c", make([]byte, 4)) // evicts b (LRU)
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should survive")
	}
	c.Set("huge", make([]byte, 11))
	if _, ok := c.Get("huge"); ok {
		t.Fatal("over-budget entry must not be cached")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be expired")
	}
	c.SetTTL("forever", []byte("x"), 0)
	now = now.Add(24 * time.Hour)
	if _, ok := c.Get("forever"); !ok {
		t.Fatal("zero TTL entry expired")
	}
	c.DeleteFunc(func(k string) bool { return k == "forever" })
	if _, ok := c.Get("forever"); ok {
		t.Fatal("DeleteFunc failed")
	}
	if st := c.Stats(); st.Hits == 0 || st.Misses == 0 {
		t.Fatalf("stats %+v", st)
	}
}
