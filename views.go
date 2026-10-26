package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type JobView struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Dest        string  `json:"dest"`
	State       string  `json:"state"`
	Created     int64   `json:"created"`
	Total       int64   `json:"total"`
	Done        int64   `json:"done"`
	Unknown     int     `json:"unknown"`
	Files       int     `json:"files"`
	FilesDone   int     `json:"filesDone"`
	FilesFailed int     `json:"filesFailed"`
	FilesActive int     `json:"filesActive"`
	Speed       float64 `json:"speed"`
	ETA         int64   `json:"eta"`
	Err         string  `json:"err,omitempty"`
}

type ItemView struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Rel     string  `json:"rel"`
	Kind    string  `json:"kind"`
	Size    int64   `json:"size"`
	Done    int64   `json:"done"`
	Ver     int64   `json:"ver"`
	State   string  `json:"state"`
	Err     string  `json:"err,omitempty"`
	ErrCode string  `json:"errCode,omitempty"`
	Wait    string  `json:"wait,omitempty"`
	Speed   float64 `json:"speed"`
}

func (j *Job) view() JobView {
	v := JobView{ID: j.ID, Title: j.Title, Dest: j.DestRoot, Created: j.Created.UnixMilli(), Files: len(j.items), ETA: -1}
	var active, queued, paused, failed, canceled int
	for _, it := range j.items {
		it.mu.Lock()
		st, size, em := it.state, it.size, it.errMsg
		it.mu.Unlock()
		d := it.done.Load()
		switch st {
		case StDone, StSkipped:
			v.FilesDone++
			if size >= 0 {
				d = size
			}
		case StVerifying:
			v.FilesActive++
			active++
			if size >= 0 {
				d = size
			}
		case StResolving, StDownloading, StWaiting:
			v.FilesActive++
			active++
		case StQueued:
			queued++
		case StPaused:
			paused++
		case StFailed:
			failed++
			v.FilesFailed++
			if v.Err == "" {
				v.Err = em
			}
		case StCanceled:
			canceled++
		}
		if size >= 0 {
			v.Total += size
			v.Done += min64(d, size)
		} else {
			v.Unknown++
		}
	}
	j.mu.Lock()
	v.Speed = j.speed
	isCanceled := j.canceled
	j.mu.Unlock()
	switch {
	case isCanceled:
		v.State = "canceled"
	case active > 0:
		v.State = "active"
	case queued > 0:
		v.State = "queued"
	case paused > 0:
		v.State = "paused"
	case failed > 0:
		v.State = "failed"
	default:
		v.State = "done"
	}
	if v.State == "active" && v.Speed > 2048 && v.Total > v.Done {
		v.ETA = int64(float64(v.Total-v.Done) / v.Speed)
	}
	return v
}

func (j *Job) itemViews() []ItemView {
	out := make([]ItemView, 0, len(j.items))
	for _, it := range j.items {
		it.mu.Lock()
		iv := ItemView{ID: it.ID, Name: it.Name, Rel: it.Rel, Kind: it.Kind, Size: it.size,
			State: string(it.state), Err: it.errMsg, ErrCode: it.errCode, Wait: it.waitMsg, Speed: it.speed}
		it.mu.Unlock()
		iv.Done = it.done.Load()
		iv.Ver = it.ver.Load()
		if (iv.State == string(StDone) || iv.State == string(StSkipped)) && iv.Size >= 0 {
			iv.Done = iv.Size
		}
		out = append(out, iv)
	}
	return out
}

func (m *Manager) Snapshot() []JobView {
	m.mu.Lock()
	jobs := append([]*Job(nil), m.jobs...)
	m.mu.Unlock()
	out := make([]JobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.view())
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	return out
}

func (m *Manager) GlobalSpeed() float64 {
	m.spMu.Lock()
	defer m.spMu.Unlock()
	return m.speed
}

func ewma(prev, inst float64) float64 {
	v := 0.35*inst + 0.65*prev
	if v < 1024 && inst < 1 {
		return 0
	}
	return v
}

// tickLoop calcula velocidades, grava o estado e avisa quando um job termina.
func (m *Manager) tickLoop(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	last, lastSave := time.Now(), time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			dt := now.Sub(last).Seconds()
			last = now
			if dt <= 0 {
				continue
			}
			cur := m.total.Load()
			m.spMu.Lock()
			m.speed = ewma(m.speed, float64(cur-m.lastTotal)/dt)
			m.lastTotal = cur
			m.spMu.Unlock()
			m.mu.Lock()
			jobs := append([]*Job(nil), m.jobs...)
			m.mu.Unlock()
			for _, j := range jobs {
				b := j.bytes.Load()
				j.mu.Lock()
				j.speed = ewma(j.speed, float64(b-j.lastBytes)/dt)
				j.lastBytes = b
				j.mu.Unlock()
				for _, it := range j.items {
					if it.getState() != StDownloading {
						continue
					}
					d := it.done.Load()
					it.mu.Lock()
					it.speed = ewma(it.speed, float64(d-it.lastDone)/dt)
					it.lastDone = d
					it.mu.Unlock()
				}
				m.checkFinished(j)
			}
			if m.dirty.Load() && now.Sub(lastSave) > 3*time.Second {
				m.dirty.Store(false)
				lastSave = now
				m.saveJobs()
			}
		}
	}
}

func (m *Manager) checkFinished(j *Job) {
	v := j.view()
	if v.State != "done" && v.State != "failed" {
		return
	}
	j.mu.Lock()
	already := j.notified
	j.notified = true
	j.mu.Unlock()
	if already {
		return
	}
	m.emit(map[string]any{"type": "jobdone", "id": j.ID, "title": j.Title, "state": v.State})
	if !m.hasUI() {
		msg := fmt.Sprintf("%s: %d arquivo(s) concluído(s).", j.Title, v.FilesDone)
		if v.State == "failed" {
			msg = fmt.Sprintf("%s: %d arquivo(s) com erro. Abra o DriveCRTV para ver.", j.Title, v.FilesFailed)
		}
		m.notify("DriveCRTV", msg)
	}
}

// ---------- persistencia ----------

type itemDisk struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Rel     string `json:"rel"`
	Kind    string `json:"kind"`
	Mime    string `json:"mime,omitempty"`
	RK      string `json:"rk,omitempty"`
	MD5     string `json:"md5,omitempty"`
	Size    int64  `json:"size"`
	State   string `json:"state"`
	Done    int64  `json:"done"`
	Err     string `json:"err,omitempty"`
	ErrCode string `json:"errCode,omitempty"`
}

type jobDisk struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	DestRoot string     `json:"destRoot"`
	Created  int64      `json:"created"`
	Items    []itemDisk `json:"items"`
}

func (m *Manager) jobsPath() string { return filepath.Join(m.dataDir, "jobs.json") }

func (m *Manager) saveJobs() {
	m.mu.Lock()
	jobs := append([]*Job(nil), m.jobs...)
	m.mu.Unlock()
	var out []jobDisk
	for _, j := range jobs {
		j.mu.Lock()
		c := j.canceled
		j.mu.Unlock()
		if c {
			continue
		}
		jd := jobDisk{ID: j.ID, Title: j.Title, DestRoot: j.DestRoot, Created: j.Created.UnixMilli()}
		for _, it := range j.items {
			it.mu.Lock()
			d := itemDisk{ID: it.ID, Name: it.Name, Rel: it.Rel, Kind: it.Kind, Mime: it.Mime, RK: it.ResourceKey,
				MD5: it.MD5, Size: it.size, State: string(it.state), Err: it.errMsg, ErrCode: it.errCode}
			it.mu.Unlock()
			d.Done = it.done.Load()
			jd.Items = append(jd.Items, d)
		}
		out = append(out, jd)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := m.jobsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.jobsPath())
	}
}

// loadJobs restaura a fila da sessao anterior; o que estava incompleto volta pausado.
func (m *Manager) loadJobs() {
	b, err := os.ReadFile(m.jobsPath())
	if err != nil {
		return
	}
	var in []jobDisk
	if json.Unmarshal(b, &in) != nil {
		return
	}
	for _, jd := range in {
		j := &Job{m: m, ID: jd.ID, Title: jd.Title, DestRoot: jd.DestRoot, Created: time.UnixMilli(jd.Created), notified: true}
		j.c, j.cancel = context.WithCancel(context.Background())
		unfinished := false
		for _, d := range jd.Items {
			st := ItemState(d.State)
			switch st {
			case StDone, StSkipped, StFailed:
			case StCanceled:
				continue
			default:
				st = StPaused
				unfinished = true
			}
			it := &Item{job: j, ID: d.ID, Name: d.Name, Rel: d.Rel, Kind: d.Kind, Mime: d.Mime,
				ResourceKey: d.RK, MD5: d.MD5, size: d.Size, state: st, errMsg: d.Err, errCode: d.ErrCode}
			it.done.Store(d.Done)
			j.items = append(j.items, it)
		}
		if len(j.items) == 0 {
			continue
		}
		if unfinished {
			j.paused = true
			j.cancel()
		}
		m.jobs = append(m.jobs, j)
	}
}
