package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// defaultAPIKey pode ser embutida em tempo de build: -ldflags "-X main.defaultAPIKey=AIza..."
var defaultAPIKey = ""

const appVersion = "1.0.0"

// Endpoints reune os hosts usados. Em testes tudo aponta para um servidor simulado.
type Endpoints struct {
	Drive       string
	UserContent string
	Docs        string
	API         string
}

func defaultEndpoints() Endpoints {
	if b := strings.TrimRight(os.Getenv("DRIVECRTV_TEST_BASE"), "/"); b != "" {
		return Endpoints{Drive: b, UserContent: b, Docs: b, API: b}
	}
	return Endpoints{
		Drive:       "https://drive.google.com",
		UserContent: "https://drive.usercontent.google.com",
		Docs:        "https://docs.google.com",
		API:         "https://www.googleapis.com",
	}
}

// Destination e um destino salvo ("Cliente X" -> /Volumes/SSD/Projetos/Cliente X/Brutos).
type Destination struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Settings struct {
	APIKey         string        `json:"apiKey"`
	ConnsPerFile   int           `json:"connsPerFile"`
	MaxConns       int           `json:"maxConns"`
	ParallelFiles  int           `json:"parallelFiles"`
	ChunkMB        int           `json:"chunkMB"`
	VerifyMD5      bool          `json:"verifyMD5"`
	WatchClipboard bool          `json:"watchClipboard"`
	Subfolder      bool          `json:"subfolder"`
	DefaultDest    string        `json:"defaultDest"`
	Destinations   []Destination `json:"destinations"`
	RecentDests    []string      `json:"recentDests"`
}

func defaultSettings() Settings {
	home, _ := os.UserHomeDir()
	return Settings{
		APIKey:         defaultAPIKey,
		ConnsPerFile:   8,
		MaxConns:       16,
		ParallelFiles:  4,
		ChunkMB:        16,
		VerifyMD5:      true,
		WatchClipboard: true,
		Subfolder:      true,
		DefaultDest:    filepath.Join(home, "Downloads"),
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *Settings) normalize() {
	s.ConnsPerFile = clampInt(s.ConnsPerFile, 1, 16)
	s.MaxConns = clampInt(s.MaxConns, 2, 64)
	s.ParallelFiles = clampInt(s.ParallelFiles, 1, 8)
	s.ChunkMB = clampInt(s.ChunkMB, 1, 64)
	s.APIKey = strings.TrimSpace(s.APIKey)
	if len(s.RecentDests) > 8 {
		s.RecentDests = s.RecentDests[:8]
	}
	if len(s.Destinations) > 50 {
		s.Destinations = s.Destinations[:50]
	}
}

// Store persiste os ajustes em <dataDir>/settings.json.
type Store struct {
	mu  sync.RWMutex
	dir string
	cur Settings
}

func dataDirPath() string {
	if d := os.Getenv("DRIVECRTV_DATA"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "DriveCRTV")
	// o app se chamava DriveJet: leva os dados antigos para a pasta nova
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		_ = os.Rename(filepath.Join(base, "DriveJet"), dir)
	}
	return dir
}

func newStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	st := &Store{dir: dir, cur: defaultSettings()}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		var s Settings
		s = st.cur
		if json.Unmarshal(b, &s) == nil {
			st.cur = s
		}
	}
	st.cur.normalize()
	return st
}

func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s := st.cur
	s.Destinations = append([]Destination(nil), st.cur.Destinations...)
	s.RecentDests = append([]string(nil), st.cur.RecentDests...)
	return s
}

func (st *Store) Update(fn func(*Settings)) Settings {
	st.mu.Lock()
	fn(&st.cur)
	st.cur.normalize()
	s := st.cur
	st.mu.Unlock()
	st.save()
	return s
}

func (st *Store) save() {
	st.mu.RLock()
	b, _ := json.MarshalIndent(st.cur, "", "  ")
	st.mu.RUnlock()
	tmp := filepath.Join(st.dir, "settings.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(st.dir, "settings.json"))
	}
}

// pushRecent coloca um destino no topo da lista de recentes.
func (s *Settings) pushRecent(p string) {
	out := []string{p}
	for _, r := range s.RecentDests {
		if r != p {
			out = append(out, r)
		}
	}
	s.RecentDests = out
}
