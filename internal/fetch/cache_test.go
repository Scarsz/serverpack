package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Scarsz/serverpack/internal/httpx"
)

func TestFetchVerifiesAndCaches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("jar-bytes"))
	}))
	defer srv.Close()
	sum := sha256.Sum256([]byte("jar-bytes"))
	want := hex.EncodeToString(sum[:])

	c := New(t.TempDir(), httpx.New())
	ctx := context.Background()
	b, err := c.Fetch(ctx, srv.URL, Expect{SHA256: want, Size: 9})
	if err != nil || b.SHA256 != want {
		t.Fatalf("fetch: %v %+v", err, b)
	}
	if _, err := c.Fetch(ctx, srv.URL, Expect{SHA256: want}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected cache hit, got %d downloads", hits.Load())
	}
	if _, err := c.Fetch(ctx, srv.URL, Expect{SHA1: "deadbeef"}); err == nil {
		t.Fatal("expected sha1 mismatch")
	}
}
