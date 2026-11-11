// SECquence: a desktop research tool for SEC filings, financials, prices, news,
// catalysts and valuation. It runs a small web server on a loopback port and shows
// its interface in a Microsoft Edge app window.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const AppName = "SECquence"

// AppVersion is replaced with the release's version when a release is built.
var AppVersion = "2.5.0"

var (
	logMu    sync.Mutex
	logger   *log.Logger
	logToStd bool
)

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	if logger != nil {
		logger.Printf(format, args...)
	}
	if logToStd {
		fmt.Printf(format+"\n", args...)
	}
}

func main() {
	selftest := flag.String("selftest", "", "check every data source for the given ticker, print the results and exit")
	email := flag.String("email", "", "contact email to use (replaces the saved setting)")
	port := flag.Int("port", 0, "port for the local server (default: any free port)")
	noWindow := flag.Bool("no-window", false, "serve without opening a window (development)")
	flag.Parse()

	cfgBase, _ := os.UserConfigDir()
	cacheBase, _ := os.UserCacheDir()
	if cfgBase == "" {
		cfgBase = "."
	}
	if cacheBase == "" {
		cacheBase = cfgBase
	}
	cfgDir, cacheDir := filepath.Join(cfgBase, AppName), filepath.Join(cacheBase, AppName)
	_ = os.MkdirAll(cfgDir, 0o755)
	_ = os.MkdirAll(cacheDir, 0o755)

	logPath := filepath.Join(cacheDir, "app.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 2<<20 {
		_ = os.Remove(logPath)
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		logger = log.New(f, "", log.LstdFlags)
	}

	cleanUpdate()
	store := OpenStore(cfgDir, filepath.Join(cfgBase, "SECFilingsViewer"))
	if *email != "" {
		_ = store.Update(func(s *Settings) { s.Email = *email })
	}
	// A session file given on the command line (dropped on the program, or chosen
	// with "Open with") becomes the session in use.
	if p := flag.Arg(0); strings.EqualFold(filepath.Ext(p), SessionExt) {
		if abs, err := filepath.Abs(p); err == nil {
			if err := store.OpenSession(abs); err != nil {
				logf("open session %s: %v", abs, err)
				store.Missing = abs
			} else {
				store.Missing = ""
			}
		}
	}
	app := &App{store: store, cacheDir: cacheDir, token: randHex(16), market: NewMarket()}
	app.sec = NewSEC(store.Email, cacheDir)
	app.printer = NewPrinter(cacheDir, app.sec.UA)
	app.dl = &Downloader{sec: app.sec, printer: app.printer, root: store.DownloadDir}

	if *selftest != "" {
		logToStd = true
		os.Exit(app.selfTest(*selftest))
	}

	// A second launch just opens a window onto the copy that is already running.
	instance := filepath.Join(cacheDir, "instance.json")
	if !*noWindow && *port == 0 {
		if url, version, running := runningInstance(instance); running {
			if version == AppVersion {
				app.openWindow(url)
				time.Sleep(2 * time.Second)
				return
			}
			// A different version is still running (usually the one just replaced by an
			// update). Ask it to stop so this copy can take over.
			if !stopInstance(url) {
				messageBox(AppName, "An older copy of SECquence is still running.\n\nClose its window, wait about 10 seconds, then start SECquence again.", true)
				os.Exit(1)
			}
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		messageBox(AppName, "SECquence couldn't start: "+err.Error(), true)
		os.Exit(1)
	}
	app.host = ln.Addr().String()
	url := fmt.Sprintf("http://%s/?t=%s", app.host, app.token)
	logf("%s %s listening on %s (browser: %q)", AppName, AppVersion, app.host, app.printer.Browser)

	srv := &http.Server{Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("server: %v", err)
		}
	}()
	if *noWindow {
		fmt.Println("Open:", url)
		select {}
	}
	b, _ := json.Marshal(map[string]string{"url": url, "version": AppVersion})
	_ = os.WriteFile(instance, b, 0o644)
	defer os.Remove(instance)
	app.waitForClose(app.openWindow(url))
	logf("window closed, exiting")
}

// runningInstance returns the address and version of an already-running copy, if it
// answers. Copies older than 2.0.2 did not record a version and report "".
func runningInstance(file string) (url, version string, running bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", "", false
	}
	var inst struct{ URL, Version string }
	if json.Unmarshal(b, &inst) != nil {
		return "", "", false
	}
	return inst.URL, inst.Version, instanceCall("GET", inst.URL, "/api/ping?probe=1") == 200
}

func instanceCall(method, instURL, apiPath string) int {
	base, token, found := strings.Cut(instURL, "/?t=")
	if !found {
		return 0
	}
	req, _ := http.NewRequest(method, base+apiPath, nil)
	req.Header.Set("X-App-Token", token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// stopInstance asks another running copy to exit and reports whether it did. Copies
// from 2.0.2 on obey /api/quit; older ones only leave once their window is closed.
func stopInstance(instURL string) bool {
	instanceCall("POST", instURL, "/api/quit")
	instanceCall("POST", instURL, "/api/bye")
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if instanceCall("GET", instURL, "/api/ping?probe=1") == 0 {
			return true
		}
	}
	return false
}

// openWindow shows the interface in an Edge app window. The returned channel closes
// when that Edge process exits after running a while, meaning the user closed it. A
// quick exit only means Edge handed the window to a process that was already running.
func (a *App) openWindow(url string) <-chan struct{} {
	closed := make(chan struct{})
	if a.printer.Browser == "" {
		_ = openDefault(url)
		return closed
	}
	cmd := exec.Command(a.printer.Browser, "--app="+url, "--user-data-dir="+filepath.Join(a.cacheDir, "window-profile"),
		"--no-first-run", "--no-default-browser-check", "--disable-sync", "--hide-crash-restore-bubble", "--window-size=1480,940")
	if err := cmd.Start(); err != nil {
		logf("open window: %v", err)
		_ = openDefault(url)
		return closed
	}
	a.window.Store(cmd.Process)
	go func() {
		began := time.Now()
		_ = cmd.Wait()
		if time.Since(began) > 8*time.Second {
			close(closed)
		}
	}()
	return closed
}

// waitForClose returns once the window is gone: its process exited, the page said
// goodbye and didn't come back, or its heartbeat stopped.
func (a *App) waitForClose(closed <-chan struct{}) {
	began := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-closed:
			time.Sleep(4 * time.Second) // a reloading page pings again within this time
			if time.Now().UnixMilli()-a.lastPing.Load() > 4000 {
				return
			}
			closed = nil
		case <-tick.C:
		}
		now, last, bye := time.Now().UnixMilli(), a.lastPing.Load(), a.byeAt.Load()
		switch {
		case a.quit.Load():
			return
		case bye > 0 && last < bye && now-bye > 6000:
			return
		case last > 0 && now-last > 3*60*1000: // a minimised window may ping only once a minute
			return
		case last == 0 && time.Since(began) > 3*time.Minute:
			messageBox(AppName, "The SECquence window did not open. Try starting the app again.", true)
			return
		}
	}
}

// selfTest exercises every data source against live services and prints what it finds.
func (a *App) selfTest(ticker string) int {
	ctx := context.Background()
	failed := 0
	check := func(step string, err error) bool {
		if err != nil {
			failed++
			logf("FAIL %-11s %v", step, err)
		}
		return err == nil
	}
	logf("%s %s self-test for %s (browser: %q)", AppName, AppVersion, ticker, a.printer.Browser)
	if a.store.Email() == "" {
		check("settings", ErrNoEmail)
		return 1
	}
	found, err := a.sec.Search(ctx, ticker)
	if err == nil && len(found) == 0 {
		err = errors.New("no company found")
	}
	if !check("search", err) {
		return 1
	}
	co := found[0]
	logf("OK   search      %s is CIK %d, %s", ticker, co.CIK, co.Name)
	_, filings, err := a.sec.Filings(ctx, co.CIK)
	if !check("filings", err) {
		return 1
	}
	count := map[string]int{}
	var latestQ *Filing
	for i := range filings {
		count[filings[i].Form]++
		if latestQ == nil && filings[i].Form == "10-Q" {
			latestQ = &filings[i]
		}
	}
	logf("OK   filings     %d filings (10-K %d, 10-Q %d, 8-K %d, Form 4 %d); newest %s on %s", len(filings), count["10-K"], count["10-Q"], count["8-K"], count["4"], filings[0].Form, filings[0].Filed)
	if latestQ != nil {
		if docs, err := a.sec.FilingDocs(ctx, co.CIK, latestQ.Acc); check("documents", err) {
			logf("OK   documents   %d files in the 10-Q filed %s", len(docs), latestQ.Filed)
		}
	}
	if fin, err := a.sec.Financials(ctx, co.CIK); check("financials", err) {
		logf("OK   financials  %d quarters, %d metrics; cash + investments $%.1fM as of %s; %d share-count points",
			len(fin.Quarterly.Columns), len(fin.Quarterly.Rows), fin.Latest["cashinv"].V/1e6, fin.Latest["cashinv"].AsOf, len(fin.ShareHistory))
		for _, key := range []string{"rd", "netinc", "cash"} {
			c, _ := fin.Quarterly.latest(key)
			if c == nil || len(c.Src) == 0 {
				continue
			}
			s := c.Src[0]
			if hit, err := a.sec.Locate(ctx, co.CIK, s.Accn, s.Concept, s.Start, s.End, s.Val); check("fact-check", err) {
				logf("OK   fact-check  %s %s: %d match(es) in %s, shown as %q, agrees=%v %s", key, s.End, len(hit.Hits), hit.DocName, hit.Shown, hit.ValueMatch, hit.Message)
			}
		}
	}
	if ins, err := a.sec.Insiders(ctx, co.CIK, 10); check("insiders", err) {
		logf("OK   insiders    %d transactions in %d Form 4s %v", len(ins.Txns), ins.Scanned, ins.Problems)
	}
	if q, err := a.market.Chart(ctx, ticker, "1d"); check("prices", err) {
		logf("OK   prices      $%.2f (previous close $%.2f), %d points today, source %s", q.Price, q.PrevClose, len(q.Points), q.Source)
	}
	if _, err := a.market.Chart(ctx, ticker, "1y"); check("prices 1y", err) {
		logf("OK   prices 1y")
	}
	if items, problems, err := a.market.News(ctx, ticker, co.Name, true); check("news", err) {
		logf("OK   news        %d articles %v", len(items), problems)
	}
	if cat, err := FindCatalysts(ctx, a.sec, a.market, co.CIK, co.Name); check("catalysts", err) {
		logf("OK   catalysts   %d guidance statements from %d documents, %d trials, %d estimated events %v", len(cat.Guidance), len(cat.Scanned), len(cat.Trials), len(cat.Events), cat.Problems)
		for _, g := range cat.Guidance[:min(5, len(cat.Guidance))] {
			logf("       %s | %s | %.110s", g.Periods[0].Label, g.Kind, g.Text)
		}
	}
	if a.printer.Browser != "" && latestQ != nil {
		out := filepath.Join(os.TempDir(), "secquence-selftest.pdf")
		began := time.Now()
		if check("pdf", a.printer.PrintToPDF(ctx, latestQ.URL, out)) {
			st, _ := os.Stat(out)
			logf("OK   pdf         %s (%d KB in %.1fs)", out, st.Size()/1024, time.Since(began).Seconds())
		}
	}
	if failed > 0 {
		logf("%d CHECK(S) FAILED", failed)
		return 1
	}
	logf("ALL OK")
	return 0
}
