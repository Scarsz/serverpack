// Package fetch implements a content-addressed download cache keyed by sha256.
package fetch

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scarsz/serverpack/internal/httpx"
)

// DefaultDir returns $SERVERPACK_CACHE, or <user cache dir>/serverpack.
func DefaultDir() string {
	if d := os.Getenv("SERVERPACK_CACHE"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "serverpack")
	}
	return filepath.Join(os.TempDir(), "serverpack-cache")
}

// Cache stores blobs at <dir>/sha256/<hex>.
type Cache struct {
	Dir  string
	HTTP *httpx.Client
}

// New returns a cache rooted at dir.
func New(dir string, client *httpx.Client) *Cache {
	return &Cache{Dir: dir, HTTP: client}
}

// Expect lists the properties a download must have. Empty fields are not checked.
type Expect struct {
	SHA256  string
	SHA1    string
	SHA512  string
	MD5     string
	Size    int64
	Headers map[string]string
}

// Blob is a verified file in the cache.
type Blob struct {
	Path   string
	SHA256 string
	SHA1   string
	SHA512 string
	MD5    string
	Size   int64
}

// Path returns where a blob with the given sha256 is stored.
func (c *Cache) Path(sha256hex string) string {
	return filepath.Join(c.Dir, "sha256", strings.ToLower(sha256hex))
}

// Lookup returns the cached blob for sha256hex, if present.
func (c *Cache) Lookup(sha256hex string) (string, bool) {
	if sha256hex == "" {
		return "", false
	}
	p := c.Path(sha256hex)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return p, true
	}
	return "", false
}

// Fetch returns the blob for url, downloading it unless a blob with exp.SHA256 is already cached.
func (c *Cache) Fetch(ctx context.Context, url string, exp Expect) (*Blob, error) {
	if p, ok := c.Lookup(exp.SHA256); ok {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		return &Blob{Path: p, SHA256: strings.ToLower(exp.SHA256), Size: st.Size()}, nil
	}
	if url == "" {
		return nil, fmt.Errorf("no download URL and sha256 %s is not cached", exp.SHA256)
	}
	resp, err := c.HTTP.Get(ctx, url, exp.Headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	blob, err := c.store(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	// The stored blob is named by its own sha256 so it stays valid in the cache even if it isn't what we expected.
	if err := verify(blob, exp); err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	return blob, nil
}

// Add copies a local file into the cache.
func (c *Cache) Add(path string) (*Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return c.store(f)
}

func (c *Cache) store(r io.Reader) (*Blob, error) {
	dir := filepath.Join(c.Dir, "sha256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	h := newHashes()
	n, err := io.Copy(io.MultiWriter(tmp, h.writer()), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	blob := h.blob(n)
	blob.Path = c.Path(blob.SHA256)
	if err := os.Rename(tmp.Name(), blob.Path); err != nil {
		// A concurrent download of the same content may have won the race.
		if _, ok := c.Lookup(blob.SHA256); !ok {
			return nil, err
		}
	}
	return blob, nil
}

func verify(b *Blob, exp Expect) error {
	check := func(name, want, got string) error {
		if want != "" && !strings.EqualFold(want, got) {
			return fmt.Errorf("%s mismatch: expected %s, got %s", name, want, got)
		}
		return nil
	}
	if err := check("sha256", exp.SHA256, b.SHA256); err != nil {
		return err
	}
	if err := check("sha1", exp.SHA1, b.SHA1); err != nil {
		return err
	}
	if err := check("sha512", exp.SHA512, b.SHA512); err != nil {
		return err
	}
	if err := check("md5", exp.MD5, b.MD5); err != nil {
		return err
	}
	if exp.Size > 0 && exp.Size != b.Size {
		return fmt.Errorf("size mismatch: expected %d bytes, got %d", exp.Size, b.Size)
	}
	return nil
}

// HashFile computes the sha256/sha1/sha512 and size of a file without caching it.
func HashFile(path string) (*Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := newHashes()
	n, err := io.Copy(h.writer(), f)
	if err != nil {
		return nil, err
	}
	b := h.blob(n)
	b.Path = path
	return b, nil
}

type hashes struct{ s256, s1, s512, m5 hash.Hash }

func newHashes() *hashes {
	return &hashes{sha256.New(), sha1.New(), sha512.New(), md5.New()}
}

func (h *hashes) writer() io.Writer { return io.MultiWriter(h.s256, h.s1, h.s512, h.m5) }

func (h *hashes) blob(size int64) *Blob {
	return &Blob{
		SHA256: hex.EncodeToString(h.s256.Sum(nil)),
		SHA1:   hex.EncodeToString(h.s1.Sum(nil)),
		SHA512: hex.EncodeToString(h.s512.Sum(nil)),
		MD5:    hex.EncodeToString(h.m5.Sum(nil)),
		Size:   size,
	}
}

// HashBytes returns the hex sha256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
