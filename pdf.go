package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Saving filings as PDF files with headless Microsoft Edge.
//
// On Windows the msedge.exe process that gets started can exit, reporting success,
// before the PDF exists, because Edge may pass the job to another of its processes.
// So a print never trusts the exit code: it uses a throwaway profile (which stops the
// job being handed to an already-running Edge), then waits for the PDF file itself to
// appear and stop growing before giving it its final name.

type Printer struct {
	Browser  string
	profiles string
	ua       func() string
	mu       sync.Mutex // one print at a time
}

func NewPrinter(cacheDir string, ua func() string) *Printer {
	p := &Printer{Browser: findBrowser(), profiles: filepath.Join(cacheDir, "print-profiles"), ua: ua}
	go func() { _ = os.RemoveAll(p.profiles) }() // left over from earlier runs
	return p
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const tmpPrefix = "~printing-"

// cleanStaleTemps removes unfinished print files older than 15 minutes.
func cleanStaleTemps(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() && strings.HasPrefix(e.Name(), tmpPrefix) && time.Since(info.ModTime()) > 15*time.Minute {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// removeLater deletes paths in the background, retrying while Edge still holds them.
func removeLater(paths ...string) {
	go func() {
		for try := 0; try < 20; try++ {
			left := 0
			for _, p := range paths {
				if os.RemoveAll(p) != nil {
					left++
				}
			}
			if left == 0 {
				return
			}
			time.Sleep(15 * time.Second)
		}
	}()
}

func renameReplace(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	return os.Rename(src, dst)
}

func (p *Printer) PrintToPDF(ctx context.Context, url, out string) error {
	if p.Browser == "" {
		return errors.New("Microsoft Edge was not found, so web pages can't be converted to PDF")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, tmpPrefix+randHex(4)+".pdf")
	profile := filepath.Join(p.profiles, randHex(4))
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	cmd := exec.Command(p.Browser,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-sync", "--disable-features=msEdgeStartupBoost",
		"--hide-scrollbars", "--mute-audio", "--run-all-compositor-stages-before-draw",
		"--no-pdf-header-footer", "--print-to-pdf-no-header",
		"--user-data-dir="+profile, "--user-agent="+p.ua(), "--print-to-pdf="+tmp, url)
	hideWindow(cmd)
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start Edge: %v", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()

	var (
		exited   bool
		exitErr  error
		exitedAt time.Time
		lastSize = int64(-1)
		steady   int
	)
	fail := func(msg string) error {
		if !exited {
			_ = cmd.Process.Kill()
		}
		removeLater(tmp, profile)
		tail := strings.TrimSpace(output.String())
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		logf("print failed for %s after %.1fs: %s (exit: %v) %s", url, time.Since(began).Seconds(), msg, exitErr, tail)
		return errors.New("PDF conversion failed: " + msg)
	}
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				_ = fail("canceled")
				return ctx.Err()
			}
			return fail("timed out waiting for Edge")
		case exitErr = <-exit:
			exited, exitedAt = true, time.Now()
			continue
		case <-tick.C:
		}
		st, err := os.Stat(tmp)
		if err != nil || st.Size() < 500 {
			// No file yet. After the process has gone, another Edge process may still
			// be writing it; wait longer when Edge reported success.
			limit := 90 * time.Second
			if exitErr != nil {
				limit = 15 * time.Second
			}
			if exited && time.Since(exitedAt) > limit {
				return fail("Edge did not produce a PDF")
			}
			continue
		}
		if st.Size() == lastSize {
			steady++
		} else {
			lastSize, steady = st.Size(), 0
		}
		// Done when the size has stopped changing (a little longer if Edge is still up).
		if (exited && steady >= 2) || steady >= 5 {
			if renameReplace(tmp, out) != nil {
				continue // still locked by Edge; try on the next tick
			}
			logf("printed %s -> %s (%d KB, %.1fs)", url, filepath.Base(out), st.Size()/1024, time.Since(began).Seconds())
			if !exited {
				select { // let Edge finish before its profile is deleted
				case <-exit:
				case <-time.After(5 * time.Second):
				}
			}
			removeLater(profile)
			return nil
		}
	}
}

// ---------------------------------------------------------------- file names

// Files are named the way the app labels them ("STOK 10-Q 2026-11-03.pdf"). A hidden
// index in each ticker folder records which SEC document each file holds, so documents
// already downloaded are skipped and two filings that would share a name (two 8-Ks on
// one day) become "... .pdf" and "... (2).pdf" instead of overwriting each other.

const indexFile = ".secquence-index.json"

var legacyIndexFiles = []string{".sec-filings-index.json"}

var indexMu sync.Mutex

func loadIndex(tickerDir string) map[string]string {
	m := map[string]string{}
	for _, name := range append([]string{indexFile}, legacyIndexFiles...) {
		if b, err := os.ReadFile(filepath.Join(tickerDir, name)); err == nil {
			old := map[string]string{}
			_ = json.Unmarshal(b, &old)
			for k, v := range old {
				if _, ok := m[k]; !ok {
					m[k] = v
				}
			}
		}
	}
	return m
}

func nonEmptyFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// resolveOutput returns where a document should be saved and whether it is already there.
func resolveOutput(tickerDir, formDir, name, key string) (string, bool) {
	indexMu.Lock()
	defer indexMu.Unlock()
	idx := loadIndex(tickerDir)
	if rel, ok := idx[key]; ok {
		if p := filepath.Join(tickerDir, rel); nonEmptyFile(p) {
			return p, true
		}
	}
	taken := map[string]bool{}
	for k, rel := range idx {
		if k != key && nonEmptyFile(filepath.Join(tickerDir, rel)) {
			taken[filepath.Clean(rel)] = true
		}
	}
	for n := 1; ; n++ {
		file := name + ".pdf"
		if n > 1 {
			file = fmt.Sprintf("%s (%d).pdf", name, n)
		}
		rel := filepath.Join(formDir, file)
		p := filepath.Join(tickerDir, rel)
		switch {
		case taken[filepath.Clean(rel)]:
			continue
		case !nonEmptyFile(p):
			return p, false
		case n == 1:
			return p, true // an unindexed file with the plain name: treat it as this document
		}
	}
}

func recordOutput(tickerDir, key, out string) {
	indexMu.Lock()
	defer indexMu.Unlock()
	rel, err := filepath.Rel(tickerDir, out)
	if err != nil {
		return
	}
	idx := loadIndex(tickerDir)
	idx[key] = rel
	b, _ := json.MarshalIndent(idx, "", " ")
	p := filepath.Join(tickerDir, indexFile)
	_ = os.Remove(p) // a hidden file can't be overwritten in place on Windows
	if os.WriteFile(p, b, 0o644) == nil {
		setHidden(p)
	}
}

func sanitizeName(s string) string {
	s = strings.NewReplacer("<", "", ">", "", ":", "-", `"`, "", "/", "-", `\`, "-", "|", "-", "?", "", "*", "").Replace(s)
	if s = strings.TrimRight(strings.TrimSpace(s), ". "); s == "" {
		return "unnamed"
	}
	return s
}

// ---------------------------------------------------------------- download jobs

type DownloadItem struct {
	CIK        int    `json:"cik"`
	Ticker     string `json:"ticker"`
	Acc        string `json:"acc"`
	Form       string `json:"form"`
	Filed      string `json:"filed"`
	PrimaryDoc string `json:"primaryDoc"`
	Exhibits   string `json:"exhibits"` // none, ex99, all
	// Set to save one document only (from the preview pane):
	DocURL  string `json:"docUrl"`
	DocType string `json:"docType"`
	DocKind string `json:"docKind"`
}

func (it DownloadItem) ticker() string {
	if it.Ticker != "" {
		return strings.ToUpper(it.Ticker)
	}
	return fmt.Sprintf("CIK%d", it.CIK)
}

// filingBaseName matches the label shown in the app: "STOK 10-Q 2026-11-03".
func filingBaseName(it DownloadItem) string {
	return fmt.Sprintf("%s %s %s", sanitizeName(it.ticker()), sanitizeName(it.Form), it.Filed)
}

func wantExhibit(d Doc, mode string) bool {
	if d.Kind != "exhibit" {
		return false
	}
	return mode == "all" || (mode == "ex99" && strings.HasPrefix(strings.ToUpper(d.Type), "EX-99"))
}

type JobResult struct {
	Label   string `json:"label"`
	File    string `json:"file"`
	Status  string `json:"status"` // saved, skipped, error
	Message string `json:"message,omitempty"`
}

type JobState struct {
	ID       string      `json:"id"`
	Total    int         `json:"total"`
	Done     int         `json:"done"`
	Current  string      `json:"current"`
	Results  []JobResult `json:"results"`
	Saved    int         `json:"saved"`
	Skipped  int         `json:"skipped"`
	Failed   int         `json:"failed"`
	Finished bool        `json:"finished"`
	Canceled bool        `json:"canceled"`
	LastFile string      `json:"lastFile"`
}

type Job struct {
	mu     sync.Mutex
	state  JobState
	cancel context.CancelFunc
}

func (j *Job) Snapshot() JobState {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.state
	s.Results = append([]JobResult{}, j.state.Results...)
	return s
}

func (j *Job) update(fn func(s *JobState)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.state)
}

func (j *Job) add(r JobResult) {
	j.update(func(s *JobState) {
		s.Results = append(s.Results, r)
		switch r.Status {
		case "saved":
			s.Saved++
			s.LastFile = r.File
		case "skipped":
			s.Skipped++
			s.LastFile = r.File
		default:
			s.Failed++
		}
	})
}

// Downloader turns filings into PDF files under a root folder.
type Downloader struct {
	sec     *SEC
	printer *Printer
	root    func() string

	mu   sync.Mutex
	jobs map[string]*Job
}

func (d *Downloader) Job(id string) *Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobs[id]
}

func (d *Downloader) Start(items []DownloadItem) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{state: JobState{ID: randHex(6), Total: len(items), Results: []JobResult{}}, cancel: cancel}
	d.mu.Lock()
	if d.jobs == nil {
		d.jobs = map[string]*Job{}
	}
	d.jobs[j.state.ID] = j
	d.mu.Unlock()
	root := d.root()
	go func() {
		defer cancel()
		for i, it := range items {
			if ctx.Err() != nil {
				break
			}
			d.saveFiling(ctx, j, it, root)
			j.update(func(s *JobState) { s.Done = i + 1 })
		}
		j.update(func(s *JobState) {
			s.Finished, s.Current = true, ""
			s.Canceled = ctx.Err() != nil && s.Done < s.Total
		})
	}()
	return j
}

type docTarget struct{ url, suffix, label string }

func (d *Downloader) saveFiling(ctx context.Context, j *Job, it DownloadItem, root string) {
	label := strings.TrimSpace(fmt.Sprintf("%s %s %s", it.ticker(), it.Form, it.Filed))
	if !reAcc.MatchString(it.Acc) {
		j.add(JobResult{Label: label, Status: "error", Message: "invalid accession number"})
		return
	}
	tickerDir := filepath.Join(root, sanitizeName(it.ticker()))
	formDir := sanitizeName(it.Form)
	cleanStaleTemps(filepath.Join(tickerDir, formDir))
	archive := d.sec.archiveBase(it.CIK, it.Acc) + "/"

	var targets []docTarget
	if it.DocURL != "" { // a single document
		if !strings.HasPrefix(it.DocURL, archive) {
			j.add(JobResult{Label: label, Status: "error", Message: "document is not part of this filing"})
			return
		}
		suffix := ""
		if it.DocKind != "main" {
			if suffix = it.DocType; suffix == "" {
				suffix = strings.TrimSuffix(path.Base(it.DocURL), path.Ext(it.DocURL))
			}
		}
		targets = append(targets, docTarget{it.DocURL, suffix, strings.TrimSpace(label + " " + suffix)})
	} else {
		var docs []Doc
		if it.PrimaryDoc == "" || (it.Exhibits != "" && it.Exhibits != "none") {
			j.update(func(s *JobState) { s.Current = label + ": reading the filing index" })
			var err error
			if docs, err = d.sec.FilingDocs(ctx, it.CIK, it.Acc); err != nil {
				what := label
				if it.PrimaryDoc != "" {
					what += " exhibits"
				}
				j.add(JobResult{Label: what, Status: "error", Message: err.Error()})
				if it.PrimaryDoc == "" {
					return
				}
			}
		}
		if it.PrimaryDoc != "" {
			targets = append(targets, docTarget{archive + it.PrimaryDoc, "", label})
		} else {
			found := false
			for _, doc := range docs {
				if doc.Kind == "main" {
					targets, found = append(targets, docTarget{doc.URL, "", label}), true
					break
				}
			}
			if !found {
				j.add(JobResult{Label: label, Status: "error", Message: "could not find the main document"})
			}
		}
		count := map[string]int{}
		for _, doc := range docs {
			if !wantExhibit(doc, it.Exhibits) {
				continue
			}
			suffix := strings.ToUpper(doc.Type)
			if count[suffix]++; count[suffix] > 1 {
				suffix = fmt.Sprintf("%s (%d)", suffix, count[suffix])
			}
			targets = append(targets, docTarget{doc.URL, suffix, label + " " + suffix})
		}
	}

	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		name := filingBaseName(it)
		if t.suffix != "" {
			name += " " + sanitizeName(t.suffix)
		}
		key := it.Acc + "|" + path.Base(t.url)
		out, have := resolveOutput(tickerDir, formDir, name, key)
		if have {
			j.add(JobResult{Label: t.label, File: out, Status: "skipped", Message: "already downloaded"})
			continue
		}
		j.update(func(s *JobState) { s.Current = t.label })
		if err := d.saveDoc(ctx, t.url, out); err != nil {
			if ctx.Err() != nil {
				return
			}
			j.add(JobResult{Label: t.label, Status: "error", Message: err.Error()})
			continue
		}
		recordOutput(tickerDir, key, out)
		j.add(JobResult{Label: t.label, File: out, Status: "saved"})
	}
}

func (d *Downloader) saveDoc(ctx context.Context, url, out string) error {
	if strings.TrimSpace(d.sec.email()) == "" {
		return ErrNoEmail
	}
	if !strings.EqualFold(path.Ext(url), ".pdf") {
		return d.printer.PrintToPDF(ctx, url, out)
	}
	b, _, err := d.sec.Fetch(ctx, url) // already a PDF: save it as it is
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}
