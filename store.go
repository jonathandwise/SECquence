package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Settings, the watchlist and saved valuation models, kept as JSON files in the
// user's config folder.

type WatchItem struct {
	Ticker        string `json:"ticker"`
	CIK           int    `json:"cik"`
	Name          string `json:"name"`
	LastSeenAcc   string `json:"lastSeenAcc"`
	LastSeenFiled string `json:"lastSeenFiled"`
}

type LastView struct {
	CIK    int    `json:"cik"`
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Tab    string `json:"tab"`
}

type Settings struct {
	Email       string      `json:"email"`
	DownloadDir string      `json:"downloadDir"`
	Exhibits    string      `json:"exhibits"` // none, ex99, all
	Theme       string      `json:"theme"`    // light, dark
	Watchlist   []WatchItem `json:"watchlist"`
	Last        LastView    `json:"last"`
	// SessionFile is the session file in use, "" for this computer's own session.
	SessionFile    string   `json:"sessionFile,omitempty"`
	RecentSessions []string `json:"recentSessions,omitempty"`
}

// A Session is one person's work: the watchlist, the saved valuation models and the
// company last open. By default it lives in the config folder ("this computer's
// session"). It can instead live in a session file, which can be copied to another
// computer; while one is open, every change is written straight to it. Settings that
// belong to the computer (email for the SEC, download folder, theme) are never in it.
type Session struct {
	App       string            `json:"app"`    // always sessionApp
	Format    int               `json:"format"` // sessionFormat
	Saved     string            `json:"saved"`
	Watchlist []WatchItem       `json:"watchlist"`
	Last      LastView          `json:"last"`
	Models    map[string]*Model `json:"models"` // by CIK
}

const (
	sessionApp    = "SECquence session"
	sessionFormat = 1
	SessionExt    = ".secq"
)

type Store struct {
	mu   sync.Mutex
	dir  string
	s    Settings
	sess *Session // the open session file's contents, nil for this computer's session
	// Missing is the session file from last time that could not be reopened at startup.
	Missing string
}

// OpenStore loads settings from dir. On first run it carries over the email,
// download folder and watchlist from SEC Filings Viewer, the app's earlier version.
func OpenStore(dir, legacyDir string) *Store {
	st := &Store{dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil && legacyDir != "" {
		b, err = os.ReadFile(filepath.Join(legacyDir, "settings.json"))
		if err == nil {
			logf("imported settings from %s", legacyDir)
		}
	}
	if err == nil {
		_ = json.Unmarshal(b, &st.s)
	}
	if st.s.DownloadDir == "" {
		st.s.DownloadDir = filepath.Join(documentsDir(), "SEC Filings")
	}
	if st.s.Exhibits == "" {
		st.s.Exhibits = "ex99"
	}
	if st.s.Theme != "dark" {
		st.s.Theme = "light"
	}
	if st.s.Watchlist == nil {
		st.s.Watchlist = []WatchItem{}
	}
	if p := st.s.SessionFile; p != "" { // carry on with the session file used last time
		if s, err := readSession(p); err == nil {
			st.sess = s
		} else {
			logf("session %s: %v", p, err)
			st.s.SessionFile = ""
			st.Missing = p
		}
	}
	return st
}

func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	c := st.s
	if st.sess != nil {
		c.Watchlist, c.Last = st.sess.Watchlist, st.sess.Last
	}
	c.Watchlist = append([]WatchItem{}, c.Watchlist...)
	c.RecentSessions = append([]string{}, st.s.RecentSessions...)
	return c
}

func (st *Store) Email() string       { return st.Get().Email }
func (st *Store) DownloadDir() string { return st.Get().DownloadDir }

func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func (st *Store) Update(fn func(s *Settings)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess == nil {
		fn(&st.s)
		return st.writeSettings()
	}
	// With a session file open, the watchlist and last view belong to the file.
	view := st.s
	view.Watchlist, view.Last = st.sess.Watchlist, st.sess.Last
	before, _ := json.Marshal(struct {
		W []WatchItem
		L LastView
	}{view.Watchlist, view.Last})
	fn(&view)
	after, _ := json.Marshal(struct {
		W []WatchItem
		L LastView
	}{view.Watchlist, view.Last})
	st.sess.Watchlist, st.sess.Last = view.Watchlist, view.Last
	view.Watchlist, view.Last = st.s.Watchlist, st.s.Last
	st.s = view
	if err := st.writeSettings(); err != nil {
		return err
	}
	if string(before) == string(after) {
		return nil
	}
	return st.writeSession()
}

func (st *Store) writeSettings() error {
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(st.dir, "settings.json"), b)
}

func (st *Store) writeSession() error {
	st.sess.App, st.sess.Format, st.sess.Saved = sessionApp, sessionFormat, time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(st.sess, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(st.s.SessionFile, b); err != nil {
		return fmt.Errorf("couldn't write the session file %s (is its drive still connected?): %w", st.s.SessionFile, err)
	}
	return nil
}

// SessionFile reports the open session file, "" for this computer's session.
func (st *Store) SessionFile() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s.SessionFile
}

func readSession(path string) (*Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if json.Unmarshal(b, &s) != nil || s.App != sessionApp {
		return nil, errors.New("that file is not a SECquence session")
	}
	if s.Format > sessionFormat {
		return nil, errors.New("that session was saved by a newer version of SECquence; update this copy to open it")
	}
	if s.Watchlist == nil {
		s.Watchlist = []WatchItem{}
	}
	if s.Models == nil {
		s.Models = map[string]*Model{}
	}
	return &s, nil
}

// useSession switches to a session file (already read or built), or back to this
// computer's session when path is "". The caller holds the lock.
func (st *Store) useSession(path string, s *Session) error {
	st.sess = s
	st.s.SessionFile = path
	if path != "" {
		recent := []string{path}
		for _, p := range st.s.RecentSessions {
			if !strings.EqualFold(p, path) && len(recent) < 8 {
				recent = append(recent, p)
			}
		}
		st.s.RecentSessions = recent
	}
	return st.writeSettings()
}

// OpenSession switches to the session in the file at path.
func (st *Store) OpenSession(path string) error {
	s, err := readSession(path)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.useSession(path, s)
}

// CloseSession goes back to this computer's own session.
func (st *Store) CloseSession() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.useSession("", nil)
}

// ForgetSession drops a file from the recent list.
func (st *Store) ForgetSession(path string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	keep := st.s.RecentSessions[:0]
	for _, p := range st.s.RecentSessions {
		if p != path {
			keep = append(keep, p)
		}
	}
	st.s.RecentSessions = keep
	_ = st.writeSettings()
}

// SaveSessionAs writes the session now in use to a new file and switches to it.
// With empty set, it starts a new, empty session in that file instead.
func (st *Store) SaveSessionAs(path string, empty bool) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := &Session{Watchlist: []WatchItem{}, Models: map[string]*Model{}}
	switch {
	case empty:
	case st.sess != nil:
		b, _ := json.Marshal(st.sess)
		_ = json.Unmarshal(b, s)
	default:
		s.Watchlist, s.Last = append(s.Watchlist, st.s.Watchlist...), st.s.Last
		files, _ := filepath.Glob(filepath.Join(st.dir, "models", "*.json"))
		for _, f := range files {
			cik, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
			if err != nil {
				continue
			}
			if m, saved := st.localModel(cik); saved {
				s.Models[strconv.Itoa(cik)] = m
			}
		}
	}
	prevFile, prev := st.s.SessionFile, st.sess
	st.sess, st.s.SessionFile = s, path
	if err := st.writeSession(); err != nil {
		st.sess, st.s.SessionFile = prev, prevFile
		return err
	}
	return st.useSession(path, s)
}

func (st *Store) Watch(cik int) (WatchItem, bool) {
	for _, w := range st.Get().Watchlist {
		if w.CIK == cik {
			return w, true
		}
	}
	return WatchItem{}, false
}

// newSince lists the filings (newest first) that are newer than the point where the
// user last opened a watched company.
func newSince(w WatchItem, filings []Filing) []string {
	out := []string{}
	if w.LastSeenAcc == "" && w.LastSeenFiled == "" {
		return out
	}
	for _, f := range filings {
		if f.Acc == w.LastSeenAcc || (w.LastSeenFiled != "" && f.Filed < w.LastSeenFiled) {
			break
		}
		out = append(out, f.Acc)
	}
	return out
}

func (st *Store) modelPath(cik int) string {
	return filepath.Join(st.dir, "models", fmt.Sprintf("%d.json", cik))
}

func (st *Store) LoadModel(cik int) (*Model, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess != nil {
		m, saved := st.sess.Models[strconv.Itoa(cik)]
		if !saved || m == nil {
			return nil, false
		}
		var c Model // a copy, so callers can't change the session behind the lock
		b, _ := json.Marshal(m)
		if json.Unmarshal(b, &c) != nil {
			return nil, false
		}
		return &c, true
	}
	return st.localModel(cik)
}

func (st *Store) localModel(cik int) (*Model, bool) {
	b, err := os.ReadFile(st.modelPath(cik))
	if err != nil {
		return nil, false
	}
	var m Model
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return &m, true
}

func (st *Store) SaveModel(cik int, m *Model) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess != nil {
		var c Model
		b, _ := json.Marshal(m)
		if err := json.Unmarshal(b, &c); err != nil {
			return err
		}
		prev, had := st.sess.Models[strconv.Itoa(cik)]
		st.sess.Models[strconv.Itoa(cik)] = &c
		if err := st.writeSession(); err != nil {
			if had {
				st.sess.Models[strconv.Itoa(cik)] = prev
			} else {
				delete(st.sess.Models, strconv.Itoa(cik))
			}
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(st.modelPath(cik), b)
}

func (st *Store) DeleteModel(cik int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sess != nil {
		delete(st.sess.Models, strconv.Itoa(cik))
		_ = st.writeSession()
		return
	}
	_ = os.Remove(st.modelPath(cik))
}
