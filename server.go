package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var webFS embed.FS

// App ties the pieces together and serves the interface on a loopback port.
type App struct {
	sec      *SEC
	market   *Market
	printer  *Printer
	dl       *Downloader
	store    *Store
	token    string // required on every API call; changes at each launch
	host     string
	cacheDir string
	lastPing atomic.Int64
	byeAt    atomic.Int64
	quit     atomic.Bool
	window   atomic.Pointer[os.Process] // the browser process showing the app window
	updater  Updater
}

type httpErr struct {
	code int
	msg  string
}

func (e *httpErr) Error() string { return e.msg }

func bad(format string, a ...any) error { return &httpErr{400, fmt.Sprintf(format, a...)} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// j adapts a function returning (value, error) into a JSON handler. Errors are 502
// (an upstream source failed) unless they carry their own status.
func j(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		if err != nil {
			code := http.StatusBadGateway
			var he *httpErr
			if errors.As(err, &he) {
				code = he.code
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, v)
	}
}

func readBody(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(v); err != nil {
		return bad("couldn't read the request: %v", err)
	}
	return nil
}

func pathCIK(r *http.Request) (int, error) {
	n, err := strconv.Atoi(strings.TrimLeft(r.PathValue("cik"), "0"))
	if err != nil || n <= 0 {
		return 0, bad("missing or invalid CIK")
	}
	return n, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /view", a.view)

	mux.HandleFunc("GET /api/ping", j(a.ping))
	mux.HandleFunc("POST /api/bye", j(func(*http.Request) (any, error) { a.byeAt.Store(time.Now().UnixMilli()); return ok, nil }))
	mux.HandleFunc("POST /api/quit", j(func(*http.Request) (any, error) { a.quit.Store(true); return ok, nil }))
	// exit is the page asking to be closed when it could not close its own window.
	mux.HandleFunc("POST /api/exit", j(func(*http.Request) (any, error) {
		go func() {
			time.Sleep(200 * time.Millisecond) // let this reply reach the page first
			if p := a.window.Load(); p != nil {
				_ = p.Kill()
			}
			a.quit.Store(true)
		}()
		return ok, nil
	}))
	mux.HandleFunc("GET /api/state", j(func(*http.Request) (any, error) { return a.state(), nil }))
	mux.HandleFunc("POST /api/settings", j(a.saveSettings))
	mux.HandleFunc("POST /api/last", j(a.saveLast))
	mux.HandleFunc("POST /api/pick-folder", j(a.pickFolder))
	mux.HandleFunc("POST /api/session", j(a.session))
	mux.HandleFunc("GET /api/update", j(func(r *http.Request) (any, error) {
		rel, err := a.updater.Check(r.Context(), r.URL.Query().Get("fresh") == "1")
		if err != nil {
			return nil, &httpErr{502, err.Error()}
		}
		return rel, nil
	}))
	// Install the newer version, start it, and leave. The new copy opens its own window.
	mux.HandleFunc("POST /api/update", j(func(r *http.Request) (any, error) {
		exe, err := a.updater.Apply(r.Context())
		if err != nil {
			return nil, &httpErr{500, err.Error()}
		}
		go func() {
			time.Sleep(300 * time.Millisecond) // let this reply reach the page first
			if p := a.window.Load(); p != nil {
				_ = p.Kill()
				time.Sleep(500 * time.Millisecond)
			}
			if err := startProgram(exe); err != nil {
				logf("restart after update: %v", err)
			}
			a.quit.Store(true)
		}()
		return ok, nil
	}))
	mux.HandleFunc("POST /api/reveal", j(a.reveal))
	mux.HandleFunc("POST /api/open-url", j(a.openURL))

	mux.HandleFunc("GET /api/search", j(func(r *http.Request) (any, error) { return a.sec.Search(r.Context(), r.URL.Query().Get("q")) }))
	mux.HandleFunc("GET /api/company/{cik}/filings", j(a.filings))
	mux.HandleFunc("GET /api/company/{cik}/docs", j(a.docs))
	mux.HandleFunc("GET /api/company/{cik}/financials", j(a.financials))
	mux.HandleFunc("POST /api/company/{cik}/financials/export", j(a.exportFinancials))
	mux.HandleFunc("GET /api/company/{cik}/locate", j(a.locate))
	mux.HandleFunc("GET /api/company/{cik}/catalysts", j(a.catalysts))
	mux.HandleFunc("GET /api/company/{cik}/insiders", j(a.insiders))
	mux.HandleFunc("GET /api/company/{cik}/model", j(a.getModel))
	mux.HandleFunc("PUT /api/company/{cik}/model", j(a.putModel))
	mux.HandleFunc("DELETE /api/company/{cik}/model", j(a.deleteModel))
	mux.HandleFunc("POST /api/company/{cik}/model/export", j(a.exportModel))
	mux.HandleFunc("POST /api/model/calc", j(a.calcModel))

	mux.HandleFunc("GET /api/chart", j(a.chart))
	mux.HandleFunc("GET /api/news", j(a.news))
	mux.HandleFunc("GET /api/peers", j(func(r *http.Request) (any, error) {
		return Peers(r.Context(), a.sec, a.market, a.store.Get().Watchlist), nil
	}))
	mux.HandleFunc("POST /api/watchlist", j(a.editWatchlist))
	mux.HandleFunc("GET /api/watchlist/status", j(a.watchStatus))

	mux.HandleFunc("POST /api/download", j(a.download))
	mux.HandleFunc("GET /api/jobs/{id}", j(a.job))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", j(a.cancelJob))
	return a.guard(mux)
}

var ok = map[string]bool{"ok": true}

// guard rejects requests from other sites: the Host header must be this app's
// loopback address (which stops DNS rebinding) and API calls must carry the token.
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.host != "" && r.Host != a.host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/view" {
			tok := r.Header.Get("X-App-Token")
			if tok == "" {
				tok = r.URL.Query().Get("t")
			}
			if tok != a.token {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) ping(r *http.Request) (any, error) {
	if r.URL.Query().Get("probe") == "" { // probes come from a second launch, not the window
		a.lastPing.Store(time.Now().UnixMilli())
	}
	return ok, nil
}

// ---------------------------------------------------------------- settings

func (a *App) state() map[string]any {
	s := a.store.Get()
	return map[string]any{"email": s.Email, "downloadDir": s.DownloadDir, "exhibits": s.Exhibits, "theme": s.Theme,
		"watchlist": s.Watchlist, "last": s.Last, "browser": a.printer.Browser, "version": AppVersion,
		"session": map[string]any{"file": s.SessionFile, "name": sessionName(s.SessionFile), "recent": s.RecentSessions, "missing": a.store.Missing}}
}

// sessionName is a session file's name without folder or extension.
func sessionName(path string) string {
	if path == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// session opens, creates or leaves a session file. Without a path, open, saveas
// and new ask for one with the system's file window.
func (a *App) session(r *http.Request) (any, error) {
	var in struct{ Action, Path string }
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	a.store.Missing = ""
	path := strings.TrimSpace(in.Path)
	switch in.Action {
	case "local":
		if err := a.store.CloseSession(); err != nil {
			return nil, &httpErr{500, err.Error()}
		}
		return a.state(), nil
	case "forget":
		a.store.ForgetSession(path)
		return a.state(), nil
	case "open", "saveas", "new":
	default:
		return nil, bad("unknown session action")
	}
	if path == "" {
		if !pickMu.TryLock() {
			return nil, &httpErr{409, "A file window is already open."}
		}
		dir := filepath.Join(documentsDir(), AppName)
		if cur := a.store.SessionFile(); cur != "" {
			dir = filepath.Dir(cur)
		} else {
			_ = os.MkdirAll(dir, 0o755)
		}
		title := map[string]string{"open": "Open a SECquence session", "saveas": "Save this session as", "new": "Where to keep the new session"}[in.Action]
		p, picked, err := pickFile(in.Action != "open", title, dir, map[string]string{"open": "", "saveas": "My session", "new": "New session"}[in.Action]+SessionExt)
		pickMu.Unlock()
		if err != nil {
			return nil, &httpErr{500, err.Error()}
		}
		if !picked {
			return map[string]any{"canceled": true}, nil
		}
		path = p
	}
	if !filepath.IsAbs(path) {
		return nil, bad("The session file needs a full path.")
	}
	path = filepath.Clean(path)
	var err error
	if in.Action == "open" {
		err = a.store.OpenSession(path)
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("can't find %s. If it is on a USB drive, plug the drive in and try again", path)
		}
	} else {
		if !strings.EqualFold(filepath.Ext(path), SessionExt) {
			path += SessionExt
		}
		err = a.store.SaveSessionAs(path, in.Action == "new")
	}
	if err != nil {
		return nil, bad("%v", err)
	}
	return a.state(), nil
}

var reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func (a *App) saveSettings(r *http.Request) (any, error) {
	var in struct {
		Email, DownloadDir, Exhibits, Theme *string
	}
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	if in.Email != nil && !reEmail.MatchString(strings.TrimSpace(*in.Email)) {
		return nil, bad("Enter a valid email address.")
	}
	if in.DownloadDir != nil {
		d := strings.TrimSpace(*in.DownloadDir)
		if !filepath.IsAbs(d) {
			return nil, bad(`The download folder must be a full path, like C:\Users\you\Documents\SEC Filings.`)
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, bad("That folder can't be used: %v", err)
		}
	}
	if in.Exhibits != nil && *in.Exhibits != "none" && *in.Exhibits != "ex99" && *in.Exhibits != "all" {
		return nil, bad("invalid exhibits option")
	}
	err := a.store.Update(func(s *Settings) {
		if in.Email != nil {
			s.Email = strings.TrimSpace(*in.Email)
		}
		if in.DownloadDir != nil {
			s.DownloadDir = filepath.Clean(strings.TrimSpace(*in.DownloadDir))
		}
		if in.Exhibits != nil {
			s.Exhibits = *in.Exhibits
		}
		if in.Theme != nil && (*in.Theme == "dark" || *in.Theme == "light") {
			s.Theme = *in.Theme
		}
	})
	if err != nil {
		return nil, &httpErr{500, "Couldn't save settings: " + err.Error()}
	}
	return a.state(), nil
}

func (a *App) saveLast(r *http.Request) (any, error) {
	var in LastView
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	_ = a.store.Update(func(s *Settings) { s.Last = in })
	return ok, nil
}

var pickMu sync.Mutex

func (a *App) pickFolder(r *http.Request) (any, error) {
	var in struct{ Initial string }
	_ = readBody(r, &in)
	if !pickMu.TryLock() {
		return nil, &httpErr{409, "The folder picker is already open."}
	}
	defer pickMu.Unlock()
	start := strings.TrimSpace(in.Initial)
	if start == "" {
		start = a.store.DownloadDir()
	}
	for start != "" { // open at the nearest folder that exists
		if st, err := os.Stat(start); err == nil && st.IsDir() {
			break
		}
		parent := filepath.Dir(start)
		if parent == start {
			start = ""
		} else {
			start = parent
		}
	}
	p, picked, err := pickFolder(start)
	if err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	return map[string]any{"path": p, "picked": picked}, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// reveal opens a file or folder inside the download folder in Explorer.
func (a *App) reveal(r *http.Request) (any, error) {
	var in struct{ Path, Ticker string }
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	root := a.store.DownloadDir()
	target := root
	if in.Path != "" {
		target = filepath.Clean(in.Path)
	} else if in.Ticker != "" {
		target = filepath.Join(root, sanitizeName(strings.ToUpper(in.Ticker)))
	}
	if !within(root, target) {
		return nil, bad("Only folders inside your download folder can be opened.")
	}
	if st, err := os.Stat(target); err == nil && !st.IsDir() {
		_ = revealFile(target)
	} else {
		_ = os.MkdirAll(target, 0o755)
		_ = openFolder(target)
	}
	return map[string]string{"path": target}, nil
}

// openURL opens a link in the default browser: sec.gov, or a link the app itself showed.
func (a *App) openURL(r *http.Request) (any, error) {
	var in struct{ URL string }
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(in.URL, "https://www.sec.gov/") && !strings.HasPrefix(in.URL, a.sec.WWW+"/") && !a.market.AllowedLink(in.URL) {
		return nil, bad("Only sec.gov links and links shown in the app can be opened.")
	}
	if err := openDefault(in.URL); err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	return ok, nil
}

// ---------------------------------------------------------------- filings

func (a *App) filings(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	comp, filings, err := a.sec.Filings(r.Context(), cik)
	if err != nil {
		return nil, err
	}
	comp.Ticker = strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("ticker")))
	if comp.Ticker == "" && len(comp.Tickers) > 0 {
		comp.Ticker = strings.ToUpper(comp.Tickers[0])
	}
	fresh := []string{}
	w, watched := a.store.Watch(cik)
	if watched {
		fresh = newSince(w, filings)
		if len(filings) > 0 { // opening the company marks its filings as seen
			_ = a.store.Update(func(s *Settings) {
				for i := range s.Watchlist {
					if s.Watchlist[i].CIK == cik {
						s.Watchlist[i].LastSeenAcc, s.Watchlist[i].LastSeenFiled = filings[0].Acc, filings[0].Filed
					}
				}
			})
		}
	}
	return map[string]any{"company": comp, "filings": filings, "newAccessions": fresh, "watched": watched}, nil
}

func (a *App) docs(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	docs, err := a.sec.FilingDocs(r.Context(), cik, r.URL.Query().Get("acc"))
	if docs == nil {
		docs = []Doc{}
	}
	return docs, err
}

// view proxies a filing document for the in-app viewer, optionally highlighting
// numbers (hit=1,2,3 are positions of <ix:nonFraction> tags).
func (a *App) view(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("url")
	if !strings.HasPrefix(u, a.sec.WWW+"/Archives/") {
		http.Error(w, "only SEC archive documents can be shown", http.StatusBadRequest)
		return
	}
	body, ct, err := a.sec.FetchCached(r.Context(), u, 24*time.Hour)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `<body style="font-family:Segoe UI,sans-serif;padding:24px;color:#555">This document couldn't be loaded: %s</body>`, htmlEscape(err.Error()))
		return
	}
	if ct == "" {
		ct = map[string]string{".htm": "text/html", ".html": "text/html", ".txt": "text/plain", ".pdf": "application/pdf"}[strings.ToLower(path.Ext(u))]
	}
	if strings.Contains(ct, "html") {
		var hits []int
		for _, p := range strings.Split(r.URL.Query().Get("hit"), ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 0 {
				hits = append(hits, n)
			}
		}
		body = injectBase(highlightIX(body, hits), u[:strings.LastIndex(u, "/")+1])
		body = keepJumpsInDoc(body, "http://"+r.Host+r.URL.RequestURI(), path.Base(u))
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------- financials

func (a *App) financials(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	return a.sec.Financials(r.Context(), cik)
}

// saveExport writes a generated file into the ticker's download folder and shows it.
func (a *App) saveExport(ticker string, cik int, name func(tk string) string, data []byte) (any, error) {
	tk := sanitizeName(strings.ToUpper(ticker))
	if ticker == "" {
		tk = fmt.Sprintf("CIK%d", cik)
	}
	file := filepath.Join(a.store.DownloadDir(), tk, name(tk))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return nil, &httpErr{500, fmt.Sprintf("Couldn't save %s. If it's open in Excel, close it and try again. (%v)", filepath.Base(file), err)}
	}
	_ = revealFile(file)
	return map[string]string{"file": file}, nil
}

func (a *App) exportFinancials(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	var in struct{ Ticker, Mode, Format string }
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	f, err := a.sec.Financials(r.Context(), cik)
	if err != nil {
		return nil, err
	}
	t, mode := f.Quarterly, "quarterly"
	if in.Mode == "annual" {
		t, mode = f.Annual, "annual"
	}
	t = t.newestFirst() // the same order as the Financials tab
	day := time.Now().Format("2006-01-02")
	if in.Format == "csv" {
		data := append([]byte("\xef\xbb\xbf"), t.CSV()...) // byte-order mark so Excel reads UTF-8
		return a.saveExport(in.Ticker, cik, func(tk string) string { return fmt.Sprintf("%s key financials %s (%s).csv", tk, mode, day) }, data)
	}
	data, err := financialsXLSX(f, t, mode, strings.ToUpper(in.Ticker), a.sec.WWW)
	if err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	return a.saveExport(in.Ticker, cik, func(tk string) string { return fmt.Sprintf("%s key financials %s (%s).xlsx", tk, mode, day) }, data)
}

func (a *App) locate(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	val, _ := strconv.ParseFloat(q.Get("val"), 64)
	return a.sec.Locate(r.Context(), cik, q.Get("accn"), q.Get("concept"), q.Get("start"), q.Get("end"), val)
}

func (a *App) catalysts(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	return FindCatalysts(r.Context(), a.sec, a.market, cik, r.URL.Query().Get("name"))
}

func (a *App) insiders(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	return a.sec.Insiders(r.Context(), cik, 60)
}

// ---------------------------------------------------------------- valuation

// tidy keeps a model received from the interface within sane bounds.
func tidy(m *Model) error {
	if m.ValuationYear < 2000 || m.ValuationYear > 2100 {
		return bad("The valuation year must be between 2000 and 2100.")
	}
	if len(m.Products) > 8 {
		return bad("A model can hold up to 8 drugs.")
	}
	m.Horizon = m.horizon()
	if m.Products == nil {
		m.Products = []Product{}
	}
	for i := range m.Products {
		p := &m.Products[i]
		if strings.TrimSpace(p.Name) == "" {
			p.Name = fmt.Sprintf("Drug %d", i+1)
		}
		p.YearsToPeak = clampPeak(p.YearsToPeak)
		if p.Share <= 0 || p.Share > 1 {
			p.Share = 1
		}
		if len(p.Base) > 200 || len(p.Src) > 200 {
			return bad("That model has too many notes.")
		}
		trimNotes(p.Src)
	}
	if len(m.Base) > 200 || len(m.Src) > 200 {
		return bad("That model has too many notes.")
	}
	trimNotes(m.Src)
	return nil
}

// trimNotes drops empty source notes and caps the length of the rest.
func trimNotes(src map[string]string) {
	for k, v := range src {
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > 4000 {
			v = string(r[:4000])
		}
		if v == "" {
			delete(src, k)
		} else {
			src[k] = v
		}
	}
}

func (a *App) getModel(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	if m, saved := a.store.LoadModel(cik); saved && tidy(m) == nil {
		m.assumeStart()
		return map[string]any{"model": m, "result": m.Calculate(), "saved": true}, nil
	}
	// No saved model: start from the filings and the clinical pipeline.
	fin, _ := a.sec.Financials(r.Context(), cik)
	var trials []Trial
	if name := cleanCompanyName(r.URL.Query().Get("name")); name != "" {
		trials, _ = a.market.Trials(r.Context(), name)
	}
	m := DefaultModel(fin, trials, time.Now().Year())
	return map[string]any{"model": m, "result": m.Calculate(), "saved": false}, nil
}

func (a *App) calcModel(r *http.Request) (any, error) {
	var m Model
	if err := readBody(r, &m); err != nil {
		return nil, err
	}
	if err := tidy(&m); err != nil {
		return nil, err
	}
	return m.Calculate(), nil
}

func (a *App) putModel(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	var m Model
	if err := readBody(r, &m); err != nil {
		return nil, err
	}
	if err := tidy(&m); err != nil {
		return nil, err
	}
	m.Placeholder = false
	m.Updated = time.Now().Format(time.RFC3339)
	if err := a.store.SaveModel(cik, &m); err != nil {
		return nil, &httpErr{500, "Couldn't save the model: " + err.Error()}
	}
	return map[string]any{"model": m, "result": m.Calculate(), "saved": true}, nil
}

func (a *App) deleteModel(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	a.store.DeleteModel(cik)
	return ok, nil
}

func (a *App) exportModel(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		Model   Model       `json:"model"`
		Ticker  string      `json:"ticker"`
		Name    string      `json:"name"`
		Price   float64     `json:"price"`
		Sources []SourceRow `json:"sources"`
	}
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	if err := tidy(&in.Model); err != nil {
		return nil, err
	}
	data, err := valuationXLSX(&in.Model, in.Model.Calculate(), in.Name, strings.ToUpper(in.Ticker), in.Price, in.Sources[:min(len(in.Sources), 2000)])
	if err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	return a.saveExport(in.Ticker, cik, func(tk string) string {
		return fmt.Sprintf("%s rNPV valuation (%s).xlsx", tk, time.Now().Format("2006-01-02"))
	}, data)
}

// ---------------------------------------------------------------- prices, news, watchlist

func (a *App) chart(r *http.Request) (any, error) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "1d"
	}
	return a.market.Chart(r.Context(), r.URL.Query().Get("ticker"), rng)
}

func (a *App) news(r *http.Request) (any, error) {
	q := r.URL.Query()
	items, problems, err := a.market.News(r.Context(), q.Get("ticker"), q.Get("name"), q.Get("fresh") != "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "problems": problems}, nil
}

func (a *App) editWatchlist(r *http.Request) (any, error) {
	var in struct {
		Action string `json:"action"`
		CIK    int    `json:"cik"`
		Ticker string `json:"ticker"`
		Name   string `json:"name"`
	}
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	switch in.Action {
	case "add":
		item := WatchItem{Ticker: strings.ToUpper(in.Ticker), CIK: in.CIK, Name: in.Name}
		if _, filings, err := a.sec.Filings(r.Context(), in.CIK); err == nil && len(filings) > 0 {
			item.LastSeenAcc, item.LastSeenFiled = filings[0].Acc, filings[0].Filed
		}
		_ = a.store.Update(func(s *Settings) {
			for _, w := range s.Watchlist {
				if w.CIK == in.CIK {
					return
				}
			}
			s.Watchlist = append(s.Watchlist, item)
		})
	case "remove":
		_ = a.store.Update(func(s *Settings) {
			kept := s.Watchlist[:0]
			for _, w := range s.Watchlist {
				if w.CIK != in.CIK {
					kept = append(kept, w)
				}
			}
			s.Watchlist = kept
		})
	default:
		return nil, bad("unknown action")
	}
	return a.store.Get().Watchlist, nil
}

// watchStatus reports each watched company's newest filing and how many are new.
func (a *App) watchStatus(r *http.Request) (any, error) {
	type status struct {
		CIK         int    `json:"cik"`
		NewCount    int    `json:"newCount"`
		LatestForm  string `json:"latestForm"`
		LatestFiled string `json:"latestFiled"`
		Error       string `json:"error,omitempty"`
	}
	out := []status{}
	for _, w := range a.store.Get().Watchlist {
		st := status{CIK: w.CIK}
		if _, filings, err := a.sec.Filings(r.Context(), w.CIK); err != nil {
			st.Error = err.Error()
		} else if len(filings) > 0 {
			st.NewCount, st.LatestForm, st.LatestFiled = len(newSince(w, filings)), filings[0].Form, filings[0].Filed
		}
		out = append(out, st)
	}
	return out, nil
}

// ---------------------------------------------------------------- downloads

func (a *App) download(r *http.Request) (any, error) {
	var in struct {
		Items []DownloadItem `json:"items"`
	}
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, bad("Nothing is selected.")
	}
	if strings.TrimSpace(a.store.Email()) == "" {
		return nil, &httpErr{400, ErrNoEmail.Error()}
	}
	return map[string]string{"id": a.dl.Start(in.Items).Snapshot().ID}, nil
}

func (a *App) job(r *http.Request) (any, error) {
	if job := a.dl.Job(r.PathValue("id")); job != nil {
		return job.Snapshot(), nil
	}
	return nil, &httpErr{404, "no such job"}
}

func (a *App) cancelJob(r *http.Request) (any, error) {
	if job := a.dl.Job(r.PathValue("id")); job != nil {
		job.cancel()
	}
	return ok, nil
}
