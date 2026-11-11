package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EDGAR client. It identifies itself with the user's contact email (an SEC
// requirement), stays under the SEC's 10 requests/second limit, retries transient
// errors, and caches responses in memory and on disk. A stale disk copy is served
// when the SEC can't be reached, so companies you've already opened still load.

var ErrNoEmail = errors.New("Add your email address in Settings first. The SEC requires every app that downloads from EDGAR to identify itself with a contact email.")

type HTTPError struct {
	Status int
	URL    string
}

func (e *HTTPError) Error() string {
	if e.Status == 404 {
		return "not found on SEC EDGAR (" + e.URL + ")"
	}
	return fmt.Sprintf("SEC returned HTTP %d for %s", e.Status, e.URL)
}

func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == 404
}

type cached struct {
	body []byte
	ct   string
	at   time.Time
}

type SEC struct {
	hc    *http.Client
	WWW   string // https://www.sec.gov
	Data  string // https://data.sec.gov
	email func() string
	dir   string // disk cache folder; "" keeps the cache in memory only

	gate sync.Mutex
	next time.Time

	mu  sync.Mutex
	mem map[string]cached

	tmu       sync.Mutex
	tickers   []TickerEntry
	tickersAt time.Time
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func NewSEC(email func() string, cacheDir string) *SEC {
	s := &SEC{
		hc:    &http.Client{Timeout: 120 * time.Second},
		WWW:   strings.TrimRight(envOr("SECQ_WWW", "https://www.sec.gov"), "/"),
		Data:  strings.TrimRight(envOr("SECQ_DATA", "https://data.sec.gov"), "/"),
		email: email,
		mem:   map[string]cached{},
	}
	if cacheDir != "" {
		s.dir = filepath.Join(cacheDir, "sec")
		_ = os.MkdirAll(s.dir, 0o755)
		go s.prune(21*24*time.Hour, 400<<20)
	}
	return s
}

func (s *SEC) UA() string { return "SECquence/2.0 " + strings.TrimSpace(s.email()) }

// wait spaces requests 125 ms apart (8 per second).
func (s *SEC) wait(ctx context.Context) error {
	s.gate.Lock()
	t := s.next
	if now := time.Now(); t.Before(now) {
		t = now
	}
	s.next = t.Add(125 * time.Millisecond)
	s.gate.Unlock()
	return sleepCtx(ctx, time.Until(t))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fetch downloads a URL from the SEC, bypassing the cache.
func (s *SEC) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if strings.TrimSpace(s.email()) == "" {
		return nil, "", ErrNoEmail
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, time.Duration(1<<(attempt-1))*time.Second); err != nil {
				return nil, "", err
			}
		}
		if err := s.wait(ctx); err != nil {
			return nil, "", err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", s.UA())
		req.Header.Set("Accept", "*/*")
		resp, err := s.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			last = fmt.Errorf("could not reach SEC EDGAR: %w", err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 400<<20))
		resp.Body.Close()
		switch {
		case err != nil:
			last = err
		case resp.StatusCode == 200:
			return body, resp.Header.Get("Content-Type"), nil
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			last = &HTTPError{resp.StatusCode, url}
		case resp.StatusCode == 403:
			return nil, "", errors.New("SEC refused the request (HTTP 403). This usually clears after a minute; also check that the email in Settings is valid")
		default:
			return nil, "", &HTTPError{resp.StatusCode, url}
		}
	}
	return nil, "", last
}

func (s *SEC) diskPath(url string) string {
	h := sha1.Sum([]byte(url))
	return filepath.Join(s.dir, hex.EncodeToString(h[:])+".c")
}

// readDisk returns a cached response and its age. The file is the content type,
// a newline, then the body.
func (s *SEC) readDisk(url string) (cached, bool) {
	if s.dir == "" {
		return cached{}, false
	}
	p := s.diskPath(url)
	st, err := os.Stat(p)
	if err != nil {
		return cached{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cached{}, false
	}
	i := strings.IndexByte(string(b[:min(len(b), 200)]), '\n')
	if i < 0 {
		return cached{}, false
	}
	return cached{body: b[i+1:], ct: string(b[:i]), at: st.ModTime()}, true
}

func (s *SEC) writeDisk(url string, c cached) {
	if s.dir == "" {
		return
	}
	p := s.diskPath(url)
	tmp := p + ".tmp"
	ct := strings.ReplaceAll(c.ct, "\n", " ")
	if os.WriteFile(tmp, append([]byte(ct+"\n"), c.body...), 0o644) == nil {
		_ = os.Remove(p)
		_ = os.Rename(tmp, p)
	}
}

// FetchCached returns a response no older than ttl, downloading it if needed.
func (s *SEC) FetchCached(ctx context.Context, url string, ttl time.Duration) ([]byte, string, error) {
	s.mu.Lock()
	c, ok := s.mem[url]
	s.mu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.body, c.ct, nil
	}
	disk, onDisk := s.readDisk(url)
	if onDisk && time.Since(disk.at) < ttl {
		s.remember(url, disk)
		return disk.body, disk.ct, nil
	}
	body, ct, err := s.Fetch(ctx, url)
	if err != nil {
		if onDisk && !errors.Is(err, ErrNoEmail) && !IsNotFound(err) && ctx.Err() == nil {
			logf("using saved copy of %s (%v)", url, err)
			return disk.body, disk.ct, nil
		}
		return nil, "", err
	}
	c = cached{body: body, ct: ct, at: time.Now()}
	s.remember(url, c)
	s.writeDisk(url, c)
	return body, ct, nil
}

func (s *SEC) remember(url string, c cached) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mem) > 250 {
		s.mem = map[string]cached{}
	}
	s.mem[url] = c
}

// prune deletes disk-cache files older than maxAge, then the oldest files until the
// cache is under maxBytes.
func (s *SEC) prune(maxAge time.Duration, maxBytes int64) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type f struct {
		p    string
		size int64
		at   time.Time
	}
	var files []f
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		p := filepath.Join(s.dir, e.Name())
		if time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(p)
			continue
		}
		files = append(files, f{p, info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, x := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(x.p) == nil {
			total -= x.size
		}
	}
}

// ---------------------------------------------------------------- ticker search

type TickerEntry struct {
	CIK    int    `json:"cik"`
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	rank   int
}

func parseTickers(body []byte) ([]TickerEntry, error) {
	var raw map[string]struct {
		CIK    int    `json:"cik_str"`
		Ticker string `json:"ticker"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("could not read the SEC ticker list: %w", err)
	}
	list := make([]TickerEntry, 0, len(raw))
	for k, v := range raw {
		r, _ := strconv.Atoi(k)
		list = append(list, TickerEntry{CIK: v.CIK, Ticker: strings.ToUpper(v.Ticker), Name: v.Title, rank: r})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].rank < list[j].rank })
	return list, nil
}

func (s *SEC) loadTickers(ctx context.Context) ([]TickerEntry, error) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.tickers != nil && time.Since(s.tickersAt) < 12*time.Hour {
		return s.tickers, nil
	}
	body, _, err := s.FetchCached(ctx, s.WWW+"/files/company_tickers.json", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	list, err := parseTickers(body)
	if err != nil {
		return nil, err
	}
	s.tickers, s.tickersAt = list, time.Now()
	return list, nil
}

func (s *SEC) Search(ctx context.Context, q string) ([]TickerEntry, error) {
	if strings.TrimSpace(q) == "" {
		return []TickerEntry{}, nil
	}
	list, err := s.loadTickers(ctx)
	if err != nil {
		return nil, err
	}
	return searchTickers(list, q, 10), nil
}

// searchTickers ranks: exact ticker, then short ticker prefixes, then company-name
// matches, then the remaining ticker prefixes. A number is treated as a CIK.
func searchTickers(list []TickerEntry, q string, limit int) []TickerEntry {
	uq := strings.ToUpper(strings.TrimSpace(q))
	tq := strings.ReplaceAll(uq, ".", "-")
	out := []TickerEntry{}
	seen := map[string]bool{}
	add := func(e TickerEntry) {
		key := e.Ticker + "|" + strconv.Itoa(e.CIK)
		if !seen[key] && len(out) < limit {
			seen[key] = true
			out = append(out, e)
		}
	}
	if n, err := strconv.Atoi(uq); err == nil && n > 0 {
		for _, e := range list {
			if e.CIK == n {
				add(e)
			}
		}
		if len(out) == 0 {
			add(TickerEntry{CIK: n, Name: "CIK " + strconv.Itoa(n)})
		}
		return out
	}
	var prefix []TickerEntry
	for _, e := range list {
		switch {
		case e.Ticker == tq:
			add(e)
		case strings.HasPrefix(e.Ticker, tq):
			prefix = append(prefix, e)
		}
	}
	sort.SliceStable(prefix, func(i, j int) bool { return len(prefix[i].Ticker) < len(prefix[j].Ticker) })
	for i, e := range prefix {
		if i < 4 {
			add(e)
		}
	}
	if len(uq) >= 2 {
		for _, e := range list {
			if strings.Contains(strings.ToUpper(e.Name), uq) {
				add(e)
			}
		}
	}
	for _, e := range prefix {
		add(e)
	}
	return out
}

// ---------------------------------------------------------------- filings

type Company struct {
	CIK           int      `json:"cik"`
	Name          string   `json:"name"`
	Ticker        string   `json:"ticker"`
	Tickers       []string `json:"tickers"`
	Exchanges     []string `json:"exchanges"`
	SIC           string   `json:"sic"`
	SICDesc       string   `json:"sicDescription"`
	FiscalYearEnd string   `json:"fiscalYearEnd"`
	State         string   `json:"stateOfIncorporation"`
	Category      string   `json:"category"`
	Website       string   `json:"website"`
}

type Filing struct {
	Acc         string `json:"acc"`
	Filed       string `json:"filed"`
	Period      string `json:"period"`
	Accepted    string `json:"accepted"`
	Form        string `json:"form"`
	Items       string `json:"items"`
	Size        int64  `json:"size"`
	PrimaryDoc  string `json:"primaryDoc"`
	PrimaryDesc string `json:"primaryDesc"`
	URL         string `json:"url"`
	IndexURL    string `json:"indexUrl"`
}

type filingCols struct {
	AccessionNumber       []string  `json:"accessionNumber"`
	FilingDate            []string  `json:"filingDate"`
	ReportDate            []string  `json:"reportDate"`
	AcceptanceDateTime    []string  `json:"acceptanceDateTime"`
	Form                  []string  `json:"form"`
	Items                 []string  `json:"items"`
	Size                  []float64 `json:"size"`
	PrimaryDocument       []string  `json:"primaryDocument"`
	PrimaryDocDescription []string  `json:"primaryDocDescription"`
}

var reAcc = regexp.MustCompile(`^\d{10}-\d{2}-\d{6}$`)

func (s *SEC) archiveBase(cik int, acc string) string {
	return fmt.Sprintf("%s/Archives/edgar/data/%d/%s", s.WWW, cik, strings.ReplaceAll(acc, "-", ""))
}

func (s *SEC) rows(cik int, c filingCols) []Filing {
	at := func(a []string, i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	out := make([]Filing, 0, len(c.AccessionNumber))
	for i, acc := range c.AccessionNumber {
		f := Filing{Acc: acc, Filed: at(c.FilingDate, i), Period: at(c.ReportDate, i), Accepted: at(c.AcceptanceDateTime, i),
			Form: at(c.Form, i), Items: at(c.Items, i), PrimaryDoc: at(c.PrimaryDocument, i), PrimaryDesc: at(c.PrimaryDocDescription, i)}
		if i < len(c.Size) {
			f.Size = int64(c.Size[i])
		}
		base := s.archiveBase(cik, acc)
		if f.PrimaryDoc != "" {
			f.URL = base + "/" + f.PrimaryDoc
		}
		f.IndexURL = base + "/" + acc + "-index.htm"
		out = append(out, f)
	}
	return out
}

// Filings returns the company's details and every filing on EDGAR, newest first.
func (s *SEC) Filings(ctx context.Context, cik int) (*Company, []Filing, error) {
	body, _, err := s.FetchCached(ctx, fmt.Sprintf("%s/submissions/CIK%010d.json", s.Data, cik), 5*time.Minute)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil, fmt.Errorf("no SEC filer found with CIK %d", cik)
		}
		return nil, nil, err
	}
	var sub struct {
		Name                 string   `json:"name"`
		Tickers              []string `json:"tickers"`
		Exchanges            []string `json:"exchanges"`
		SIC                  string   `json:"sic"`
		SICDescription       string   `json:"sicDescription"`
		FiscalYearEnd        string   `json:"fiscalYearEnd"`
		StateOfIncorporation string   `json:"stateOfIncorporation"`
		Category             string   `json:"category"`
		Website              string   `json:"website"`
		Filings              struct {
			Recent filingCols `json:"recent"`
			Files  []struct {
				Name string `json:"name"`
			} `json:"files"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &sub); err != nil {
		return nil, nil, fmt.Errorf("could not read the SEC filing list: %w", err)
	}
	comp := &Company{CIK: cik, Name: sub.Name, Tickers: sub.Tickers, Exchanges: sub.Exchanges, SIC: sub.SIC, SICDesc: sub.SICDescription,
		FiscalYearEnd: sub.FiscalYearEnd, State: sub.StateOfIncorporation, Category: sub.Category, Website: sub.Website}
	all := s.rows(cik, sub.Filings.Recent)
	for _, f := range sub.Filings.Files { // older filings are split into extra pages
		if f.Name == "" || strings.ContainsAny(f.Name, `/\`) {
			continue
		}
		b, _, err := s.FetchCached(ctx, s.Data+"/submissions/"+f.Name, 12*time.Hour)
		if err != nil {
			logf("older filings page %s: %v", f.Name, err)
			continue
		}
		var cols filingCols
		if json.Unmarshal(b, &cols) == nil {
			all = append(all, s.rows(cik, cols)...)
		}
	}
	seen := map[string]bool{}
	uniq := all[:0]
	for _, f := range all {
		if !seen[f.Acc] {
			seen[f.Acc] = true
			uniq = append(uniq, f)
		}
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		if uniq[i].Filed != uniq[j].Filed {
			return uniq[i].Filed > uniq[j].Filed
		}
		return uniq[i].Accepted > uniq[j].Accepted
	})
	return comp, uniq, nil
}

// ---------------------------------------------------------------- documents in a filing

type Doc struct {
	Seq         string `json:"seq"`
	Description string `json:"description"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	Kind        string `json:"kind"` // main, exhibit, graphic, data, full, other
}

var (
	reTable       = regexp.MustCompile(`(?is)<table[^>]*class="tableFile"[^>]*>(.*?)</table>`)
	reRow         = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reCell        = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
	reHref        = regexp.MustCompile(`(?is)href\s*=\s*"([^"]+)"`)
	reTag         = regexp.MustCompile(`(?s)<[^>]+>`)
	reExhibitName = regexp.MustCompile(`(?i)ex[-_]?(\d{1,2})(?:[-_.](\d{1,2})|(\d))?\D`)
	reRPage       = regexp.MustCompile(`^R\d+\.htm$`)
	reFullTxt     = regexp.MustCompile(`^\d{10}-\d{2}-\d{6}\.txt$`)
)

// cellText strips tags and collapses whitespace.
func cellText(s string) string {
	s = html.UnescapeString(reTag.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
}

func isPrintable(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".htm", ".html", ".txt", ".pdf":
		return true
	}
	return false
}

func classifyDoc(d Doc, mainAllowed bool) string {
	t := strings.ToUpper(strings.TrimSpace(d.Type))
	ext := strings.ToLower(path.Ext(d.Name))
	switch {
	case strings.Contains(strings.ToLower(d.Description), "complete submission"):
		return "full"
	case t == "GRAPHIC" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".png":
		return "graphic"
	case strings.HasPrefix(t, "EX-101") || t == "ZIP" || t == "XML" || t == "JSON" || ext == ".xsd" || ext == ".zip" || ext == ".json" || ext == ".js" || ext == ".css":
		return "data"
	case strings.HasPrefix(t, "EX-"):
		if isPrintable(d.Name) {
			return "exhibit"
		}
		return "data"
	case d.Seq == "1" && mainAllowed:
		return "main"
	case isPrintable(d.Name):
		return "other"
	}
	return "data"
}

// parseIndex reads the document tables of a filing's "-index.htm" page.
func parseIndex(page, www string) []Doc {
	var docs []Doc
	haveMain := false
	for _, t := range reTable.FindAllStringSubmatch(page, -1) {
		for _, r := range reRow.FindAllStringSubmatch(t[1], -1) {
			cells := reCell.FindAllStringSubmatch(r[1], -1)
			hm := reHref.FindStringSubmatch(r[1])
			if len(cells) < 4 || hm == nil {
				continue
			}
			href := strings.TrimPrefix(html.UnescapeString(hm[1]), "/ix?doc=") // inline-XBRL viewer link
			if strings.HasPrefix(href, "/") {
				href = www + href
			}
			d := Doc{Seq: cellText(cells[0][1]), Description: cellText(cells[1][1]), Type: cellText(cells[3][1]), URL: href}
			if f := strings.Fields(cellText(cells[2][1])); len(f) > 0 {
				d.Name = f[0]
			} else {
				d.Name = path.Base(href)
			}
			if len(cells) >= 5 {
				d.Size, _ = strconv.ParseInt(cellText(cells[4][1]), 10, 64)
			}
			d.Kind = classifyDoc(d, !haveMain)
			haveMain = haveMain || d.Kind == "main"
			docs = append(docs, d)
		}
	}
	return docs
}

// parseDirJSON reads a filing folder's index.json. It carries no document types, so
// exhibits are recognised by file name.
func parseDirJSON(b []byte, base string) ([]Doc, error) {
	var dir struct {
		Directory struct {
			Item []struct {
				Name string          `json:"name"`
				Type string          `json:"type"`
				Size json.RawMessage `json:"size"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(b, &dir); err != nil {
		return nil, err
	}
	var docs []Doc
	for _, it := range dir.Directory.Item {
		low := strings.ToLower(it.Name)
		if it.Type == "dir" || !isPrintable(it.Name) || reRPage.MatchString(it.Name) || strings.HasSuffix(low, "-index.htm") || strings.HasSuffix(low, "-index-headers.html") {
			continue
		}
		d := Doc{Name: it.Name, URL: base + "/" + it.Name, Kind: "other"}
		d.Size, _ = strconv.ParseInt(strings.Trim(string(it.Size), `"`), 10, 64)
		if reFullTxt.MatchString(it.Name) {
			d.Kind, d.Description = "full", "Complete submission text file"
		} else if m := reExhibitName.FindStringSubmatch(it.Name); m != nil {
			d.Kind, d.Type = "exhibit", "EX-"+m[1]
			if sub := m[2] + m[3]; sub != "" {
				d.Type += "." + sub
			}
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// FilingDocs lists the documents in a filing.
func (s *SEC) FilingDocs(ctx context.Context, cik int, acc string) ([]Doc, error) {
	if !reAcc.MatchString(acc) {
		return nil, fmt.Errorf("invalid accession number %q", acc)
	}
	base := s.archiveBase(cik, acc)
	body, _, err := s.FetchCached(ctx, base+"/"+acc+"-index.htm", 24*time.Hour)
	if err == nil {
		if docs := parseIndex(string(body), s.WWW); len(docs) > 0 {
			return docs, nil
		}
	} else if errors.Is(err, ErrNoEmail) {
		return nil, err
	}
	b, _, err2 := s.FetchCached(ctx, base+"/index.json", 24*time.Hour)
	if err2 != nil {
		if err != nil {
			return nil, err
		}
		return nil, err2
	}
	return parseDirJSON(b, base)
}
