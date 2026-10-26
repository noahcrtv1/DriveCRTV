package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var bufPool = sync.Pool{New: func() any { b := make([]byte, 512<<10); return &b }}

// session guarda o endereco direto de um arquivo e o renova quando expira.
type session struct {
	f      *Fetcher
	ref    ItemRef
	client *http.Client
	mu     sync.Mutex
	res    *Resolved
	gen    int
}

func newSession(f *Fetcher, ref ItemRef) *session {
	return &session{f: f, ref: ref, client: f.client()}
}

func (s *session) resolve(ctx context.Context) (*Resolved, error) {
	res, err := s.f.Resolve(ctx, s.ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.res = res
	s.gen++
	s.mu.Unlock()
	return res, nil
}

func (s *session) current() (*Resolved, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res, s.gen
}

func (s *session) refresh(ctx context.Context, gen int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return nil
	}
	res, err := s.f.Resolve(ctx, s.ref)
	if err != nil {
		return err
	}
	s.res = res
	s.gen++
	return nil
}

func (it *Item) actualFinal() string {
	it.mu.Lock()
	p := it.finalP
	it.mu.Unlock()
	if p != "" {
		return p
	}
	return it.finalPath()
}

func nextFreePath(p string) string {
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for n := 1; ; n++ {
		c := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
}

func pickChunk(size int64, conns int, maxChunk int64) int64 {
	c := size / int64(conns*4)
	if c < 1<<20 {
		c = 1 << 20
	}
	if c > maxChunk {
		c = maxChunk
	}
	return c
}

func chunkBounds(idx int, chunk, size int64) (int64, int64) {
	start := int64(idx) * chunk
	return start, min64(start+chunk, size) - 1
}

// ---------- estado de retomada ----------

type chunkState struct {
	ID    string   `json:"id"`
	Size  int64    `json:"size"`
	Chunk int64    `json:"chunk"`
	Done  [][2]int `json:"done"`
}

func rangesFromBitmap(b []bool) [][2]int {
	var out [][2]int
	for i := 0; i < len(b); i++ {
		if !b[i] {
			continue
		}
		j := i
		for j+1 < len(b) && b[j+1] {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

func applyRanges(b []bool, r [][2]int) {
	for _, p := range r {
		for i := p[0]; i <= p[1] && i < len(b); i++ {
			if i >= 0 {
				b[i] = true
			}
		}
	}
}

// ---------- fluxo de um arquivo ----------

func (m *Manager) downloadOnce(ctx context.Context, it *Item) error {
	cfg := m.st.Get()
	it.set(StResolving)
	it.done.Store(0)
	ref := ItemRef{ID: it.ID, Name: it.Name, Kind: it.Kind, Mime: it.Mime, ResourceKey: it.ResourceKey, Size: it.getSize(), MD5: it.MD5}
	sess := newSession(m.f, ref)
	res, err := sess.resolve(ctx)
	if err != nil {
		return err
	}
	size := res.Size
	if size >= 0 {
		it.setSize(size)
	}
	if res.MD5 != "" && it.MD5 == "" {
		it.MD5 = res.MD5
	}

	final := it.finalPath()
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	part := final + ".drivecrtv"
	if fi, err := os.Stat(final); err == nil && !fi.IsDir() {
		if size >= 0 && fi.Size() == size {
			it.mu.Lock()
			it.finalP = final
			it.mu.Unlock()
			it.done.Store(size)
			it.set(StSkipped)
			return nil
		}
		if _, perr := os.Stat(part); perr != nil {
			final = nextFreePath(final)
			part = final + ".drivecrtv"
		}
	}
	statePath := final + ".drivecrtv.json"
	it.mu.Lock()
	it.finalP = final
	it.mu.Unlock()
	it.set(StDownloading)

	var derr2 error
	switch {
	case size == 0:
		f, e := os.Create(part)
		if e != nil {
			return e
		}
		f.Close()
	case res.Ranges && size > 0:
		derr2 = m.rangedDownload(ctx, it, sess, res, part, statePath, cfg)
		var de *DriveError
		if errors.As(derr2, &de) && de.Code == ErrNoRange {
			derr2 = m.streamDownload(ctx, it, sess, part, statePath)
		}
	default:
		derr2 = m.streamDownload(ctx, it, sess, part, statePath)
	}
	if derr2 != nil {
		return derr2
	}

	if sz := it.getSize(); sz >= 0 {
		if fi, e := os.Stat(part); e != nil || fi.Size() != sz {
			_ = os.Remove(part)
			_ = os.Remove(statePath)
			return derr(ErrIntegrity, "O tamanho do arquivo baixado não confere. Baixando de novo.", true)
		}
	}
	if cfg.VerifyMD5 && len(it.MD5) == 32 {
		select {
		case m.verifyCh <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		it.ver.Store(0)
		it.set(StVerifying)
		sum, e := md5File(ctx, part, &it.ver)
		<-m.verifyCh
		if e != nil {
			return e
		}
		if !strings.EqualFold(sum, it.MD5) {
			_ = os.Remove(part)
			_ = os.Remove(statePath)
			return derr(ErrIntegrity, "A verificação de integridade (MD5) falhou. O arquivo será baixado de novo.", true)
		}
	}
	if err := os.Rename(part, final); err != nil {
		return err
	}
	if !res.ModTime.IsZero() {
		_ = os.Chtimes(final, time.Now(), res.ModTime)
	}
	_ = os.Remove(statePath)
	if sz := it.getSize(); sz >= 0 {
		it.done.Store(sz)
	}
	it.set(StDone)
	return nil
}

func md5File(ctx context.Context, path string, prog *atomic.Int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	buf := make([]byte, 4<<20)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			prog.Add(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------- download em blocos paralelos ----------

func (m *Manager) rangedDownload(ctx context.Context, it *Item, s *session, res *Resolved, part, statePath string, cfg Settings) error {
	size := res.Size
	chunk := pickChunk(size, cfg.ConnsPerFile, int64(cfg.ChunkMB)<<20)
	var st chunkState
	resume := false
	if b, err := os.ReadFile(statePath); err == nil && json.Unmarshal(b, &st) == nil &&
		st.ID == it.ID && st.Size == size && st.Chunk > 0 {
		if fi, err := os.Stat(part); err == nil && fi.Size() == size {
			resume = true
			chunk = st.Chunk
		}
	}
	n := int(ceilDiv(size, chunk))
	done := make([]bool, n)
	if resume {
		applyRanges(done, st.Done)
	} else {
		_ = os.Remove(statePath)
	}
	var doneBytes int64
	remaining := 0
	for i, d := range done {
		if d {
			a, b := chunkBounds(i, chunk, size)
			doneBytes += b - a + 1
		} else {
			remaining++
		}
	}
	if free := freeBytes(filepath.Dir(part)); free >= 0 && !resume && free < size+(64<<20) {
		return derr(ErrDisk, fmt.Sprintf("Espaço insuficiente no destino (livre: %s, necessário: %s).", humanBytes(free), humanBytes(size)), false)
	}
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if !resume {
		if err := f.Truncate(0); err != nil {
			return err
		}
		if err := f.Truncate(size); err != nil {
			return err
		}
	}
	it.done.Store(doneBytes)

	var mu sync.Mutex
	save := func() {
		mu.Lock()
		r := rangesFromBitmap(done)
		mu.Unlock()
		b, _ := json.Marshal(chunkState{ID: it.ID, Size: size, Chunk: chunk, Done: r})
		tmp := statePath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, statePath)
		}
	}
	save()

	if remaining > 0 {
		pending := make(chan int, remaining)
		for i := 0; i < n; i++ {
			if !done[i] {
				pending <- i
			}
		}
		close(pending)
		workers := cfg.ConnsPerFile
		if workers > remaining {
			workers = remaining
		}
		wctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					save()
				case <-stop:
					return
				}
			}
		}()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range pending {
					if wctx.Err() != nil {
						return
					}
					if err := m.fetchChunk(wctx, it, s, f, idx, chunk, size); err != nil {
						errOnce.Do(func() {
							firstErr = err
							cancel()
						})
						return
					}
					mu.Lock()
					done[idx] = true
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		close(stop)
		save()
		if firstErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return firstErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return f.Close()
}

func (m *Manager) fetchChunk(ctx context.Context, it *Item, s *session, f *os.File, idx int, chunk, size int64) error {
	start, end := chunkBounds(idx, chunk, size)
	off := start
	backoff := retryUnit
	for attempt := 0; ; attempt++ {
		err := m.tryRange(ctx, it, s, f, &off, end)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		de := asDriveError(err)
		if !de.Retryable || attempt >= 6 {
			return de
		}
		wait := backoff + time.Duration(rand.Intn(400))*retryUnit/1000
		if de.Code == ErrRate && wait < 5*retryUnit {
			wait = 5 * retryUnit
		}
		if !sleepCtx(ctx, wait) {
			return ctx.Err()
		}
		if backoff < 20*retryUnit {
			backoff *= 2
		}
	}
}

func parseContentRangeStart(cr string) int64 {
	cr = strings.TrimSpace(strings.TrimPrefix(cr, "bytes"))
	if i := strings.IndexAny(cr, "-/"); i > 0 {
		if v, err := strconv.ParseInt(strings.TrimSpace(cr[:i]), 10, 64); err == nil {
			return v
		}
	}
	return -1
}

func looksLikeHTML(b []byte) bool {
	h := bytes.ToLower(b)
	if len(h) > 2048 {
		h = h[:2048]
	}
	return bytes.Contains(h, []byte("<html")) || bytes.Contains(h, []byte("<!doctype html"))
}

func (m *Manager) tryRange(ctx context.Context, it *Item, s *session, f *os.File, off *int64, end int64) error {
	res, gen := s.current()
	sem := m.connSem()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-sem }()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(45*time.Second, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, res.URL, nil)
	if err != nil {
		return derr(ErrOther, "Endereço de download inválido.", false)
	}
	setCommon(req)
	for k, v := range res.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", *off, end))
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return netErr(err)
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	switch {
	case resp.StatusCode == http.StatusPartialContent && !isHTML(ct):
		if cr := resp.Header.Get("Content-Range"); cr != "" && parseContentRangeStart(cr) != *off {
			return derr(ErrNet, "O servidor devolveu um trecho diferente do pedido.", true)
		}
	case resp.StatusCode == http.StatusOK && !isHTML(ct):
		return derr(ErrNoRange, "O servidor não aceita downloads em partes.", false)
	default:
		return m.chunkStatusError(ctx, s, res, gen, resp)
	}

	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	for {
		stall.Reset(45 * time.Second)
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if left := end + 1 - *off; int64(n) > left {
				n = int(left)
			}
			if _, werr := f.WriteAt(buf[:n], *off); werr != nil {
				return werr
			}
			*off += int64(n)
			it.done.Add(int64(n))
			it.job.bytes.Add(int64(n))
			m.addBytes(int64(n))
		}
		if rerr == io.EOF || *off > end {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return netErr(rerr)
		}
	}
	if *off != end+1 {
		return derr(ErrNet, "Resposta incompleta do servidor.", true)
	}
	return nil
}

// chunkStatusError traduz uma resposta que nao e dado de arquivo.
func (m *Manager) chunkStatusError(ctx context.Context, s *session, res *Resolved, gen int, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	code := resp.StatusCode
	if res.API {
		e := mapAPIError(code, body)
		if e.Code == ErrBadKey {
			s.f.apiBad.Store(true)
			e.Retryable = true
		}
		return e
	}
	renew := func() error {
		if err := s.refresh(ctx, gen); err != nil {
			return err
		}
		return derr(ErrExpired, "O link temporário de download expirou ou o Google recusou o acesso. Tentando de novo.", true)
	}
	if isHTML(resp.Header.Get("Content-Type")) || looksLikeHTML(body) {
		// status 200 de proposito: num bloco de arquivo que ja abriu antes, um 403 e quase sempre link expirado
		if _, e := s.f.interpretHTML(string(body), resp.Request.URL, 200); e != nil {
			if de := asDriveError(e); de.Code != ErrHTML {
				return de
			}
		}
		return renew()
	}
	switch code {
	case 401, 403, 404, 410:
		return renew()
	case 416:
		return derr(ErrOther, "O arquivo mudou no Drive durante o download. Tente de novo.", false)
	}
	return statusErr(code)
}

// ---------- download em fluxo unico (docs exportados ou servidor sem Range) ----------

func (m *Manager) streamDownload(ctx context.Context, it *Item, s *session, part, statePath string) error {
	_ = os.Remove(statePath)
	res, gen := s.current()
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	it.done.Store(0)

	sem := m.connSem()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-sem }()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(60*time.Second, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, res.URL, nil)
	if err != nil {
		return derr(ErrOther, "Endereço de download inválido.", false)
	}
	setCommon(req)
	for k, v := range res.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return netErr(err)
	}
	defer resp.Body.Close()
	if strings.HasSuffix(resp.Request.URL.Hostname(), "accounts.google.com") {
		return errPrivate()
	}
	if resp.StatusCode != http.StatusOK || isHTML(resp.Header.Get("Content-Type")) {
		return m.chunkStatusError(ctx, s, res, gen, resp)
	}
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	var written int64
	for {
		stall.Reset(60 * time.Second)
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			it.done.Store(written)
			it.job.bytes.Add(int64(n))
			m.addBytes(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return netErr(rerr)
		}
	}
	if cl := resp.ContentLength; cl >= 0 && written != cl {
		return derr(ErrNet, "Download incompleto.", true)
	}
	if known := it.getSize(); known >= 0 && known != written {
		return derr(ErrIntegrity, "O tamanho baixado difere do esperado.", true)
	}
	it.setSize(written)
	return f.Close()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
