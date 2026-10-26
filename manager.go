package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ItemState string

const (
	StQueued      ItemState = "queued"
	StResolving   ItemState = "resolving"
	StDownloading ItemState = "downloading"
	StVerifying   ItemState = "verifying"
	StWaiting     ItemState = "waiting"
	StDone        ItemState = "done"
	StSkipped     ItemState = "skipped"
	StFailed      ItemState = "failed"
	StPaused      ItemState = "paused"
	StCanceled    ItemState = "canceled"
)

type Item struct {
	job         *Job
	ID          string
	Name        string
	Rel         string
	Kind        string
	Mime        string
	ResourceKey string
	MD5         string

	mu       sync.Mutex
	size     int64
	state    ItemState
	errMsg   string
	errCode  string
	waitMsg  string
	finalP   string
	speed    float64
	lastDone int64

	done    atomic.Int64
	ver     atomic.Int64
	running atomic.Bool
	requeue atomic.Bool
}

func (it *Item) set(st ItemState) {
	it.mu.Lock()
	it.state = st
	if st != StWaiting {
		it.waitMsg = ""
	}
	if st != StFailed {
		it.errMsg, it.errCode = "", ""
	}
	if st != StDownloading {
		it.speed = 0
	}
	it.mu.Unlock()
	it.job.m.dirty.Store(true)
}

func (it *Item) setWait(msg string) {
	it.mu.Lock()
	it.state, it.waitMsg = StWaiting, msg
	it.mu.Unlock()
	it.job.m.dirty.Store(true)
}

func (it *Item) fail(de *DriveError) {
	it.mu.Lock()
	it.state, it.errMsg, it.errCode = StFailed, de.Msg, de.Code
	it.speed = 0
	it.mu.Unlock()
	it.job.m.dirty.Store(true)
}

func (it *Item) getState() ItemState {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.state
}

func (it *Item) getSize() int64 {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.size
}

func (it *Item) setSize(n int64) {
	it.mu.Lock()
	it.size = n
	it.mu.Unlock()
}

func (it *Item) finalPath() string {
	return filepath.Join(it.job.DestRoot, filepath.FromSlash(it.Rel))
}

type Job struct {
	m        *Manager
	ID       string
	Title    string
	DestRoot string
	Created  time.Time
	items    []*Item

	mu       sync.Mutex
	c        context.Context
	cancel   context.CancelFunc
	paused   bool
	canceled bool
	notified bool

	bytes     atomic.Int64
	speed     float64
	lastBytes int64
}

func (j *Job) ctx() context.Context {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.c
}

type Manager struct {
	st *Store
	f  *Fetcher

	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []*Job
	pending []*Item
	closing bool

	semMu     sync.RWMutex
	connCh    chan struct{}
	fileCh    chan struct{}
	verifyCh  chan struct{}
	dirty     atomic.Bool
	total     atomic.Int64
	spMu      sync.Mutex
	speed     float64
	lastTotal int64

	dataDir string
	emit    func(v map[string]any)
	notify  func(title, msg string)
	hasUI   func() bool
}

func newManager(st *Store, f *Fetcher, dataDir string) *Manager {
	m := &Manager{st: st, f: f, dataDir: dataDir, verifyCh: make(chan struct{}, 2)}
	m.cond = sync.NewCond(&m.mu)
	m.emit = func(map[string]any) {}
	m.notify = func(string, string) {}
	m.hasUI = func() bool { return true }
	m.applySettings()
	return m
}

func (m *Manager) applySettings() {
	s := m.st.Get()
	m.semMu.Lock()
	if m.connCh == nil || cap(m.connCh) != s.MaxConns {
		m.connCh = make(chan struct{}, s.MaxConns)
	}
	if m.fileCh == nil || cap(m.fileCh) != s.ParallelFiles {
		m.fileCh = make(chan struct{}, s.ParallelFiles)
	}
	m.semMu.Unlock()
}

func (m *Manager) connSem() chan struct{} {
	m.semMu.RLock()
	defer m.semMu.RUnlock()
	return m.connCh
}

func (m *Manager) fileSem() chan struct{} {
	m.semMu.RLock()
	defer m.semMu.RUnlock()
	return m.fileCh
}

func (m *Manager) addBytes(n int64) { m.total.Add(n) }

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- criacao de jobs ----------

type AddItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Mime        string `json:"mime"`
	ResourceKey string `json:"resourceKey"`
	MD5         string `json:"md5"`
	Size        int64  `json:"size"`
}

type AddReq struct {
	Title     string    `json:"title"`
	Dest      string    `json:"dest"`
	Subfolder bool      `json:"subfolder"`
	Items     []AddItem `json:"items"`
}

func (m *Manager) AddJob(req AddReq) (*Job, error) {
	if !filepath.IsAbs(req.Dest) {
		return nil, errors.New("escolha uma pasta de destino")
	}
	if len(req.Items) == 0 {
		return nil, errors.New("nenhum arquivo selecionado")
	}
	if len(req.Items) > 200000 {
		return nil, errors.New("itens demais")
	}
	title := strings.TrimSpace(req.Title)
	root := req.Dest
	if req.Subfolder && title != "" {
		root = filepath.Join(req.Dest, sanitizeName(title))
	}
	if title == "" {
		if len(req.Items) == 1 {
			title = req.Items[0].Name
		} else {
			title = fmt.Sprintf("%d arquivos", len(req.Items))
		}
	}
	j := &Job{m: m, ID: newID(), Title: title, DestRoot: root, Created: time.Now()}
	j.c, j.cancel = context.WithCancel(context.Background())
	taken := map[string]bool{}
	for _, a := range req.Items {
		if a.ID == "" {
			continue
		}
		rel := cleanRel(a.Path)
		if rel == "" {
			rel = sanitizeName(a.Name)
		}
		it := &Item{job: j, ID: a.ID, Name: a.Name, Rel: uniqueRel(taken, rel), Kind: a.Kind,
			Mime: a.Mime, ResourceKey: a.ResourceKey, MD5: a.MD5, size: a.Size, state: StQueued}
		if a.Kind == "" {
			it.Kind = string(KindFile)
		}
		j.items = append(j.items, it)
	}
	if len(j.items) == 0 {
		return nil, errors.New("nenhum arquivo válido")
	}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	m.mu.Unlock()
	m.st.Update(func(s *Settings) { s.pushRecent(req.Dest) })
	m.dirty.Store(true)
	m.enqueue(j.items...)
	return j, nil
}

func (m *Manager) enqueue(items ...*Item) {
	m.mu.Lock()
	m.pending = append(m.pending, items...)
	m.mu.Unlock()
	m.cond.Broadcast()
}

func (m *Manager) findJob(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// ---------- despacho ----------

func (m *Manager) dispatchLoop() {
	for {
		m.mu.Lock()
		for len(m.pending) == 0 && !m.closing {
			m.cond.Wait()
		}
		if m.closing {
			m.mu.Unlock()
			return
		}
		it := m.pending[0]
		m.pending = m.pending[1:]
		m.mu.Unlock()
		if it.getState() != StQueued || it.running.Load() {
			continue
		}
		ch := m.fileSem()
		ch <- struct{}{}
		if it.getState() != StQueued || it.job.ctx().Err() != nil {
			<-ch
			continue
		}
		it.running.Store(true)
		go func() {
			defer func() { <-ch }()
			m.runItem(it)
		}()
	}
}

func (m *Manager) runItem(it *Item) {
	j := it.job
	ctx := j.ctx()
	defer func() {
		it.running.Store(false)
		if it.requeue.CompareAndSwap(true, false) {
			it.set(StQueued)
			m.enqueue(it)
		}
	}()
	for attempt := 0; ; attempt++ {
		err := m.downloadOnce(ctx, it)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			j.mu.Lock()
			cancelled := j.canceled
			j.mu.Unlock()
			if cancelled {
				cleanupPartial(it)
				it.set(StCanceled)
			} else {
				it.set(StPaused)
			}
			return
		}
		de := asDriveError(err)
		if !de.Retryable || attempt >= 7 {
			it.fail(de)
			return
		}
		wait := time.Duration(min64(int64(3<<attempt), 45)) * retryUnit
		it.setWait(fmt.Sprintf("%s — nova tentativa em %ds", de.Msg, int(wait.Seconds())))
		if !sleepCtx(ctx, wait) {
			it.set(StPaused)
			return
		}
	}
}

func cleanupPartial(it *Item) {
	p := it.actualFinal()
	_ = os.Remove(p + ".drivecrtv")
	_ = os.Remove(p + ".drivecrtv.json")
}

// ---------- acoes ----------

func (m *Manager) Pause(id string) bool {
	j := m.findJob(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	if j.paused || j.canceled {
		j.mu.Unlock()
		return true
	}
	j.paused = true
	j.cancel()
	j.mu.Unlock()
	for _, it := range j.items {
		switch it.getState() {
		case StQueued, StWaiting:
			if !it.running.Load() {
				it.set(StPaused)
			}
		}
	}
	m.dirty.Store(true)
	return true
}

func (m *Manager) Resume(id string) bool {
	j := m.findJob(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	if j.canceled {
		j.mu.Unlock()
		return false
	}
	wasPaused := j.paused
	if wasPaused {
		j.c, j.cancel = context.WithCancel(context.Background())
		j.paused = false
	}
	j.notified = false
	j.mu.Unlock()
	var re []*Item
	for _, it := range j.items {
		st := it.getState()
		if st == StDone || st == StSkipped || st == StCanceled {
			continue
		}
		if it.running.Load() {
			if wasPaused {
				it.requeue.Store(true)
			}
			continue
		}
		if st == StPaused || st == StFailed {
			it.set(StQueued)
			re = append(re, it)
		}
	}
	m.enqueue(re...)
	m.dirty.Store(true)
	return true
}

func (m *Manager) Cancel(id string) bool {
	j := m.findJob(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	j.canceled = true
	j.cancel()
	j.mu.Unlock()
	for _, it := range j.items {
		st := it.getState()
		if st == StDone || st == StSkipped {
			continue
		}
		if !it.running.Load() {
			cleanupPartial(it)
			it.set(StCanceled)
		}
	}
	m.dirty.Store(true)
	return true
}

func (m *Manager) Remove(id string) bool {
	j := m.findJob(id)
	if j == nil {
		return false
	}
	m.Cancel(id)
	m.mu.Lock()
	for i, x := range m.jobs {
		if x == j {
			m.jobs = append(m.jobs[:i:i], m.jobs[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.dirty.Store(true)
	return true
}

func (m *Manager) ClearFinished() {
	m.mu.Lock()
	var keep []*Job
	for _, j := range m.jobs {
		if v := j.view(); v.State == "done" || v.State == "canceled" {
			continue
		}
		keep = append(keep, j)
	}
	m.jobs = keep
	m.mu.Unlock()
	m.dirty.Store(true)
}

// HasActive diz se ha trabalho em andamento ou na fila.
func (m *Manager) HasActive() bool {
	m.mu.Lock()
	jobs := append([]*Job(nil), m.jobs...)
	m.mu.Unlock()
	for _, j := range jobs {
		switch j.view().State {
		case "active", "queued":
			return true
		}
	}
	return false
}

// Shutdown pausa tudo e grava o estado para retomar depois.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing = true
	jobs := append([]*Job(nil), m.jobs...)
	m.mu.Unlock()
	m.cond.Broadcast()
	for _, j := range jobs {
		j.mu.Lock()
		if !j.canceled {
			j.paused = true
			j.cancel()
		}
		j.mu.Unlock()
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, j := range jobs {
			for _, it := range j.items {
				if it.running.Load() {
					busy = true
				}
			}
		}
		if !busy {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.saveJobs()
}
