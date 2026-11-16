package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const userAgent = "potluck"

// HTTP/1.1 only, so every parallel stream is its own TCP connection. Some PCs cap the speed
// of a single connection (a Windows TCP setting capped ours at about 1 MB/s); many separate
// connections get around that, one multiplexed HTTP/2 connection would not.
var dlClient = &http.Client{Transport: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	DialContext:         (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	MaxIdleConnsPerHost: 64,
	TLSHandshakeTimeout: 20 * time.Second,
}}

var apiClient = &http.Client{Timeout: 40 * time.Second}

func httpGetJSON(u string, out any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	res, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s answered %s", hostOf(u), res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return u
}

func rangeGet(ctx context.Context, u string, start, end int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	res, err := dlClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, fmt.Errorf("%s answered %s to a range request", hostOf(u), res.Status)
	}
	return res, nil
}

// remoteSize asks how big a file is without downloading it.
func remoteSize(ctx context.Context, u string) (int64, error) {
	res, err := rangeGet(ctx, u, 0, 0)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	cr := res.Header.Get("Content-Range") // bytes 0-0/12345
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		var n int64
		if _, err := fmt.Sscan(cr[i+1:], &n); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, errors.New("server did not report the file size")
}

// fetchHeader downloads just enough of a GGUF file to read its header.
func fetchHeader(ctx context.Context, u string) (*ModelInfo, error) {
	return fetchHeaderPart(ctx, u, 0)
}

// fetchModelHeaders reads a model's header, or all of them when it comes as several files.
func fetchModelHeaders(ctx context.Context, m *ModelRef) (*ModelInfo, error) {
	if len(m.Parts) < 2 {
		return fetchHeader(ctx, m.URL)
	}
	infos := make([]*ModelInfo, len(m.Parts))
	for i, p := range m.Parts {
		hint := 0
		if i > 0 {
			hint = infos[0].Layers
		}
		info, err := fetchHeaderPart(ctx, p.URL, hint)
		if err != nil {
			return nil, fmt.Errorf("file %d of %d: %w", i+1, len(m.Parts), err)
		}
		infos[i] = info
	}
	return mergeParts(infos), nil
}

func fetchHeaderPart(ctx context.Context, u string, layersHint int) (*ModelInfo, error) {
	size, err := remoteSize(ctx, u)
	if err != nil {
		return nil, err
	}
	for n := int64(8 << 20); n <= 1<<30; n *= 4 {
		if n > size {
			n = size
		}
		res, err := rangeGet(ctx, u, 0, n-1)
		if err != nil {
			return nil, err
		}
		buf, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		m, err := parseGGUFPart(buf, size, layersHint)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, errShort) || n == size {
			return nil, err
		}
	}
	return nil, errors.New("model header is larger than 1 GB")
}

// Progress reports how far a long job is.
type Progress func(done, total int64, note string)

// downloadFile fetches a big file over many parallel connections straight into its final
// place, resumes after an interruption, and checks the SHA-256 at the end when one is known.
func downloadFile(ctx context.Context, u, dest string, size int64, sha string, streams int, prog Progress) error {
	if st, err := os.Stat(dest); err == nil && st.Size() == size {
		if _, err := os.Stat(dest + ".ok"); err == nil {
			return nil // already downloaded and verified
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	const chunk = 32 << 20
	nChunks := int((size + chunk - 1) / chunk)
	stateFile := dest + ".progress"
	done := make([]bool, nChunks)
	if b, err := os.ReadFile(stateFile); err == nil && len(b) == nChunks {
		if st, err := os.Stat(dest + ".part"); err == nil && st.Size() == size {
			for i := range done {
				done[i] = b[i] == '1'
			}
		}
	}
	f, err := os.OpenFile(dest+".part", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("not enough disk space for %s: %w", humanBytes(size), err)
	}
	var got atomic.Int64
	queue := make(chan int, nChunks)
	for i, d := range done {
		if d {
			got.Add(chunkLen(i, chunk, size))
		} else {
			queue <- i
		}
	}
	close(queue)
	var mu sync.Mutex
	saveState := func() {
		b := make([]byte, nChunks)
		for i, d := range done {
			b[i] = '0'
			if d {
				b[i] = '1'
			}
		}
		_ = os.WriteFile(stateFile, b, 0o644)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr error
	var wg sync.WaitGroup
	if streams < 1 {
		streams = 1
	}
	for s := 0; s < streams; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 1<<20)
			for i := range queue {
				start := int64(i) * chunk
				n := chunkLen(i, chunk, size)
				var err error
				for attempt := 0; attempt < 8; attempt++ {
					if ctx.Err() != nil {
						return
					}
					var written int64
					written, err = fetchRange(ctx, u, f, start, n, buf, &got)
					if err == nil {
						break
					}
					got.Add(-written)
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Duration(2+attempt*3) * time.Second):
					}
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
				done[i] = true
				saveState()
				mu.Unlock()
			}
		}()
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if prog != nil {
					prog(got.Load(), size, "downloading")
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := f.Sync(); err != nil {
		return err
	}
	f.Close()
	if sha != "" {
		if prog != nil {
			prog(size, size, "checking the download")
		}
		sum, err := sha256File(dest + ".part")
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, sha) {
			_ = os.Remove(stateFile)
			return fmt.Errorf("the download is damaged (checksum %s, expected %s); it will be fetched again next time", sum[:12], sha[:12])
		}
	}
	if err := os.Rename(dest+".part", dest); err != nil {
		return err
	}
	_ = os.Remove(stateFile)
	note := "verified"
	if sha == "" {
		note = "no checksum was published for this file"
	}
	return os.WriteFile(dest+".ok", []byte(note+"\n"), 0o644)
}

func chunkLen(i int, chunk, size int64) int64 {
	if rem := size - int64(i)*chunk; rem < chunk {
		return rem
	}
	return chunk
}

func fetchRange(ctx context.Context, u string, f *os.File, start, n int64, buf []byte, got *atomic.Int64) (int64, error) {
	res, err := rangeGet(ctx, u, start, start+n-1)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var written int64
	for written < n {
		// A stalled connection should not hang the whole download.
		timer := time.AfterFunc(90*time.Second, func() { res.Body.Close() })
		k, err := res.Body.Read(buf)
		timer.Stop()
		if k > 0 {
			if int64(k) > n-written {
				k = int(n - written)
			}
			if _, werr := f.WriteAt(buf[:k], start+written); werr != nil {
				return written, werr
			}
			written += int64(k)
			got.Add(int64(k))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
	}
	if written != n {
		return written, fmt.Errorf("connection ended early (%d of %d bytes)", written, n)
	}
	return written, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 4<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------- worker cache seeding ----------

// The worker program keeps every tensor above 10 MiB in a cache folder, named by the FNV-1a
// hash of its bytes; when the file is there the head sends only the hash. Filling that cache
// straight from the download source means the head never has to push the worker's share of
// the model across the local network.
const rpcHashThreshold = 10 << 20

type fnv64 struct{ h uint64 }

func (f *fnv64) Write(p []byte) {
	h := f.h
	for _, b := range p {
		h ^= uint64(b)
		h *= 0x100000001b3
	}
	f.h = h
}

// SeedItem is one byte range of the model file that belongs in a worker's cache.
type SeedItem struct {
	URL  string `json:"url,omitempty"` // the file it is in, when the model comes as several files
	Off  int64  `json:"off"`
	Size int64  `json:"size"`
}

func seedCache(ctx context.Context, u, cacheDir string, items []SeedItem, jobs int, prog Progress) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	// Remember what has been seeded from which source, so a restart does not fetch it again.
	indexFile := filepath.Join(cacheDir, "seeded.json")
	index := map[string]string{}
	if b, err := os.ReadFile(indexFile); err == nil {
		_ = json.Unmarshal(b, &index)
	}
	src := func(it SeedItem) string {
		if it.URL != "" {
			return it.URL
		}
		return u
	}
	key := func(it SeedItem) string { return fmt.Sprintf("%s#%d+%d", src(it), it.Off, it.Size) }
	var total, done atomic.Int64
	var todo []SeedItem
	for _, it := range items {
		total.Add(it.Size)
		if name, ok := index[key(it)]; ok {
			if st, err := os.Stat(filepath.Join(cacheDir, name)); err == nil && st.Size() == it.Size {
				done.Add(it.Size)
				continue
			}
		}
		todo = append(todo, it)
	}
	if jobs < 1 {
		jobs = 1
	}
	queue := make(chan SeedItem, len(todo))
	for _, it := range todo {
		queue <- it
	}
	close(queue)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for j := 0; j < jobs; j++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			buf := make([]byte, 1<<20)
			for it := range queue {
				var name string
				var err error
				for attempt := 0; attempt < 6; attempt++ {
					if ctx.Err() != nil {
						return
					}
					var n int64
					name, n, err = seedOne(ctx, src(it), cacheDir, it, j, buf, &done)
					if err == nil {
						break
					}
					done.Add(-n)
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Duration(2+attempt*3) * time.Second):
					}
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
				index[key(it)] = name
				b, _ := json.Marshal(index)
				_ = os.WriteFile(indexFile, b, 0o644)
				mu.Unlock()
			}
		}(j)
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if prog != nil {
					prog(done.Load(), total.Load(), "fetching this computer's share")
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	if firstErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if firstErr != nil {
		return firstErr
	}
	// The cache now holds exactly this computer's share of this model. Anything else in it is
	// left over from an earlier model or layout: remove it so the folder never grows past one share.
	mu.Lock()
	defer mu.Unlock()
	keepName := map[string]bool{"seeded.json": true}
	keepKey := map[string]string{}
	for _, it := range items {
		if name, ok := index[key(it)]; ok {
			keepName[name] = true
			keepKey[key(it)] = name
		}
	}
	if ents, err := os.ReadDir(cacheDir); err == nil {
		for _, e := range ents {
			if !e.IsDir() && !keepName[e.Name()] {
				_ = os.Remove(filepath.Join(cacheDir, e.Name()))
			}
		}
	}
	b, _ := json.Marshal(keepKey)
	_ = os.WriteFile(indexFile, b, 0o644)
	return nil
}

func seedOne(ctx context.Context, u, cacheDir string, it SeedItem, j int, buf []byte, done *atomic.Int64) (string, int64, error) {
	res, err := rangeGet(ctx, u, it.Off, it.Off+it.Size-1)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	tmp := filepath.Join(cacheDir, fmt.Sprintf(".part-%d", j))
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	h := fnv64{h: 0xcbf29ce484222325}
	var n int64
	for n < it.Size {
		timer := time.AfterFunc(90*time.Second, func() { res.Body.Close() })
		k, rerr := res.Body.Read(buf)
		timer.Stop()
		if k > 0 {
			if int64(k) > it.Size-n {
				k = int(it.Size - n)
			}
			h.Write(buf[:k])
			if _, werr := f.Write(buf[:k]); werr != nil {
				f.Close()
				return "", n, werr
			}
			n += int64(k)
			done.Add(int64(k))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", n, rerr
		}
	}
	f.Close()
	if n != it.Size {
		return "", n, fmt.Errorf("connection ended early (%d of %d bytes)", n, it.Size)
	}
	name := fmt.Sprintf("%016x", h.h)
	if err := os.Rename(tmp, filepath.Join(cacheDir, name)); err != nil {
		return "", n, err
	}
	return name, n, nil
}
