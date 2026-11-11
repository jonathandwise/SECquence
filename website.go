package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Website resources: the files a company posts on its own website that are not
// filed with the SEC, such as conference posters, scientific presentations and
// investor slide decks. Every company's site is built differently, so this reads a
// handful of likely pages (investors, events and presentations, science,
// publications, pipeline) and collects the documents they link to. It is a best
// effort: pages that only fill in with JavaScript, or sites that turn away
// automated visitors, yield little, and the user can add pages by hand.

type SiteDoc struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"` // Presentation, Poster, Publication, Fact sheet, Report, Other
	Date     string `json:"date"` // YYYY-MM-DD, YYYY-MM or ""
	URL      string `json:"url"`
	FileType string `json:"fileType"` // PDF, PPTX, ...
	Page     string `json:"page"`     // the page it was found on
	PageName string `json:"pageName"`
}

type SiteScan struct {
	Site     string    `json:"site"`
	Source   string    `json:"source"` // where the address came from: "you", "SEC", "press release", ""
	Pages    []string  `json:"pages"`  // pages read
	Extra    []string  `json:"extra"`  // pages the user added
	Docs     []SiteDoc `json:"docs"`
	Scanned  string    `json:"scanned"`
	Problems []string  `json:"problems"`
}

type Sites struct {
	hc  *http.Client
	dir string
	mu  sync.Mutex
	mem map[int]*SiteScan
}

func NewSites(cacheDir string) *Sites {
	s := &Sites{hc: &http.Client{Timeout: 15 * time.Second}, mem: map[int]*SiteScan{}}
	if cacheDir != "" {
		s.dir = filepath.Join(cacheDir, "sites")
		_ = os.MkdirAll(s.dir, 0o755)
	}
	return s
}

const siteUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 SECquence"

func (s *Sites) get(ctx context.Context, u string, limit int64) ([]byte, http.Header, string, error) {
	return s.getWith(s.hc, ctx, u, limit)
}

func (s *Sites) getWith(hc *http.Client, ctx context.Context, u string, limit int64) ([]byte, http.Header, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", siteUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.Header, "", fmt.Errorf("%s answered %s", hostOf(u), resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(b)) > limit {
		return nil, nil, "", errors.New("larger than expected")
	}
	return b, resp.Header, resp.Request.URL.String(), nil
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Hostname())
}

// baseDomain is the last two labels of a host ("investor.acme.com" -> "acme.com"),
// or three for the common two-part country endings.
func baseDomain(host string) string {
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) <= 2 {
		return host
	}
	n := 2
	if last2 := parts[len(parts)-2]; len(parts[len(parts)-1]) == 2 && (last2 == "co" || last2 == "com" || last2 == "org" || last2 == "ac") {
		n = 3
	}
	return strings.Join(parts[len(parts)-n:], ".")
}

// normalizeSite turns what a person types ("acme.com", "www.acme.com/investors")
// into an address, or "" when it is not one.
func normalizeSite(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.Contains(u.Hostname(), ".") && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// ------------------------------------------------------------------ reading a page

type pageLink struct {
	href string   // absolute
	text string   // the link's own words
	ctx  []string // text just before it, nearest last, back to the previous document link
}

var (
	reDrop      = regexp.MustCompile(`(?is)<!--.*?-->|<script\b.*?</script>|<style\b.*?</style>|<noscript\b.*?</noscript>|<svg\b.*?</svg>`)
	reSiteTag   = regexp.MustCompile(`(?s)<(/?)([a-zA-Z][a-zA-Z0-9]*)((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	reLinkHref  = regexp.MustCompile(`(?is)\bhref\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)
	rePageTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reRunSpaces = regexp.MustCompile(`\s+`)
	reDocExt    = regexp.MustCompile(`(?i)\.(pdf|pptx?|ppsx?|key|docx?|xlsx?)$`)
	reGeneric   = regexp.MustCompile(`(?i)^(view|download|open|read|see|click here( for| to)?|get|pdf|link|here|learn more|more|full)?\s*(the\s+)?(full\s+)?(pdf|poster|presentation|slides?|deck|article|editorial|publication|abstract|paper|file|document|report|webcast|materials?|supporting materials?|here)?\s*(\(?pdf\)?)?\s*(\([\d.,]+\s*[kmg]b\))?$`)
)

func cleanText(s string) string {
	return strings.TrimSpace(reRunSpaces.ReplaceAllString(html.UnescapeString(s), " "))
}

// parsePage pulls the title and every link out of a page, each link with the text
// that led up to it. Documents on these sites are often linked with words like
// "View poster", with the real title a line or two above; that text is the context.
func parsePage(body []byte, pageURL string) (title string, links []pageLink) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", nil
	}
	src := reDrop.ReplaceAllString(string(body), " ")
	if m := rePageTitle.FindStringSubmatch(src); m != nil {
		title = cleanText(m[1])
	}
	var ctx []string
	var inA bool
	var href string
	var text strings.Builder
	addText := func(t string) {
		if t = cleanText(t); t == "" {
			return
		}
		if inA {
			text.WriteString(t + " ")
			return
		}
		if ctx = append(ctx, t); len(ctx) > 8 {
			ctx = ctx[len(ctx)-8:]
		}
	}
	pos := 0
	for _, m := range reSiteTag.FindAllStringSubmatchIndex(src, -1) {
		addText(src[pos:m[0]])
		pos = m[1]
		closing, name, attrs := src[m[2]:m[3]] == "/", strings.ToLower(src[m[4]:m[5]]), src[m[6]:m[7]]
		if name != "a" {
			continue
		}
		if !closing {
			inA, href = true, ""
			text.Reset()
			if h := reLinkHref.FindStringSubmatch(attrs); h != nil {
				href = strings.Trim(h[1], `"'`)
			}
			continue
		}
		if !inA {
			continue
		}
		inA = false
		t := cleanText(text.String())
		ref, err := url.Parse(strings.TrimSpace(html.UnescapeString(href)))
		if href == "" || err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		abs.Fragment = ""
		l := pageLink{href: abs.String(), text: t, ctx: append([]string{}, ctx...)}
		links = append(links, l)
		if isDocURL(abs) {
			ctx = ctx[:0] // the next document starts its own context
		} else if t != "" {
			if ctx = append(ctx, t); len(ctx) > 8 {
				ctx = ctx[len(ctx)-8:]
			}
		}
	}
	return title, links
}

// isDocURL reports whether an address looks like a file rather than a page:
// a document extension, or the "static-files" pattern investor sites use.
func isDocURL(u *url.URL) bool {
	p := strings.ToLower(u.Path)
	return reDocExt.MatchString(p) || strings.Contains(p, "/static-files/")
}

// ------------------------------------------------------------------ describing a document

var (
	siteMonths = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	reDateMDY  = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(20\d{2})\b`)
	reDateDMY  = regexp.MustCompile(`(?i)\b(\d{1,2})\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?,?\s+(20\d{2})\b`)
	reDateISO  = regexp.MustCompile(`\b(20\d{2})-(\d{2})-(\d{2})\b`)
	reDateNum  = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(20\d{2})\b`)
	reDateMY   = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?,?\s+(20\d{2})\b`)
	reSkipDoc  = regexp.MustCompile(`(?i)charter|code of (business )?(conduct|ethics)|polic(y|ies)|privacy|terms of use|guidelines|bylaws|by-laws|certificate of incorporation|whistleblower|cookie|committee|insider trading|clawback|form of |job description|application form|consent form|proxy card|10-[kq]\b|form 8-k|annual report on form`)
	rePoster   = regexp.MustCompile(`(?i)\bposter`)
	rePresent  = regexp.MustCompile(`(?i)presentation|slides?\b|\bdeck\b|business update|corporate overview|company overview|investor day|r&d day|webcast|oral\b|symposium|kol event`)
	rePublic   = regexp.MustCompile(`(?i)publication|article|editorial|manuscript|journal|\bnejm\b|lancet|nature|\bpaper\b|abstract|reprint`)
	reFact     = regexp.MustCompile(`(?i)fact ?sheet|backgrounder|brochure|infographic|one-pager|overview`)
	reReport   = regexp.MustCompile(`(?i)annual report|proxy statement|esg|sustainability|impact report`)
	reFileWord = regexp.MustCompile(`[-_+]+|%20`)
)

func findDate(texts ...string) string {
	for _, t := range texts {
		if m := reDateMDY.FindStringSubmatch(t); m != nil {
			return fmt.Sprintf("%s-%02d-%02s", m[3], siteMonths[strings.ToLower(m[1])], pad2(m[2]))
		}
		if m := reDateDMY.FindStringSubmatch(t); m != nil {
			return fmt.Sprintf("%s-%02d-%s", m[3], siteMonths[strings.ToLower(m[2])], pad2(m[1]))
		}
		if m := reDateISO.FindStringSubmatch(t); m != nil {
			return m[0]
		}
		if m := reDateNum.FindStringSubmatch(t); m != nil {
			return fmt.Sprintf("%s-%s-%s", m[3], pad2(m[1]), pad2(m[2]))
		}
	}
	for _, t := range texts {
		if m := reDateMY.FindStringSubmatch(t); m != nil {
			return fmt.Sprintf("%s-%02d", m[2], siteMonths[strings.ToLower(m[1])])
		}
	}
	return ""
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

func fileWords(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	name := path.Base(p.Path)
	name = reDocExt.ReplaceAllString(name, "")
	if un, err := url.PathUnescape(name); err == nil {
		name = un
	}
	return strings.TrimSpace(reFileWord.ReplaceAllString(name, " "))
}

func fileType(u, contentType string) string {
	if m := reDocExt.FindStringSubmatch(strings.ToLower(pathOf(u))); m != nil {
		return strings.ToUpper(m[1])
	}
	switch ct := strings.ToLower(contentType); {
	case strings.Contains(ct, "pdf"):
		return "PDF"
	case strings.Contains(ct, "presentation"), strings.Contains(ct, "powerpoint"):
		return "PPTX"
	case strings.Contains(ct, "word"):
		return "DOCX"
	case strings.Contains(ct, "sheet"), strings.Contains(ct, "excel"):
		return "XLSX"
	}
	return ""
}

func pathOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Path
}

// describe turns a link to a file into a titled, dated, classified document, or
// reports false for files that say nothing about the company's value (charters,
// policies and the like).
func describe(l pageLink, pageURL, pageName string) (SiteDoc, bool) {
	words := fileWords(l.href)
	title := l.text
	if reGeneric.MatchString(title) || len([]rune(title)) < 8 {
		title = ""
		for i := len(l.ctx) - 1; i >= 0; i-- { // the nearest substantial line above the link
			c := l.ctx[i]
			if len([]rune(c)) >= 20 && !reGeneric.MatchString(c) && findDateOnly(c) == "" {
				title = c
				break
			}
		}
		if title == "" && !strings.Contains(l.href, "/static-files/") {
			title = words
		}
		if title == "" {
			title = strings.TrimSpace(l.text)
		}
	}
	if r := []rune(title); len(r) > 260 {
		title = string(r[:257]) + "…"
	}
	all := l.text + " " + title + " " + words
	if title == "" || reSkipDoc.MatchString(all) {
		return SiteDoc{}, false
	}
	near := make([]string, 0, len(l.ctx)+3)
	near = append(near, l.text, title)
	for i := len(l.ctx) - 1; i >= 0; i-- {
		near = append(near, l.ctx[i])
	}
	near = append(near, words)
	d := SiteDoc{Title: title, URL: l.href, Page: pageURL, PageName: pageName, Date: findDate(near...), FileType: fileType(l.href, "")}
	where := pageURL + " " + pageName
	switch {
	case rePoster.MatchString(all):
		d.Kind = "Poster"
	case rePresent.MatchString(all):
		d.Kind = "Presentation"
	case rePublic.MatchString(all):
		d.Kind = "Publication"
	case reReport.MatchString(all):
		d.Kind = "Report"
	case reFact.MatchString(all):
		d.Kind = "Fact sheet"
	case rePoster.MatchString(where):
		d.Kind = "Poster"
	case rePresent.MatchString(where):
		d.Kind = "Presentation"
	case rePublic.MatchString(where):
		d.Kind = "Publication"
	default:
		d.Kind = "Other"
	}
	return d, true
}

// findDateOnly returns a date when the text is little more than a date.
func findDateOnly(s string) string {
	if len([]rune(s)) > 40 {
		return ""
	}
	return findDate(s)
}

// ------------------------------------------------------------------ choosing pages

var (
	reWantPage   = regexp.MustCompile(`(?i)invest|event|presentation|publication|poster|scien|pipeline|platform|technolog|research|resource|media|program|clinical|product|therap|medicine|data|librar|congress|conference`)
	reStrongPage = regexp.MustCompile(`(?i)event|presentation|publication|poster|congress|conference|scientific|librar|resource`)
	reSkipPage   = regexp.MustCompile(`(?i)career|job|privacy|terms|cookie|contact|sitemap|login|sign-?in|press-release|news-release|/news/.{25,}|sec-filing|financial|stock|governance|analyst|email-alert|faq|leadership|team|board|director|rss|calendar|\.ics|mailto:|/node/\d+|settlement|subscribe|unsubscribe|\bsearch\b|/tag/|/category/|/author/`)
)

// Scan reads a company's website and lists the documents it links to.
func (s *Sites) Scan(ctx context.Context, site string, extra []string) *SiteScan {
	out := &SiteScan{Site: site, Extra: extra, Docs: []SiteDoc{}, Pages: []string{}, Problems: []string{}, Scanned: time.Now().Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	home := baseDomain(hostOf(site))
	allowed := func(u string) bool { h := hostOf(u); return h != "" && baseDomain(h) == home }

	type job struct {
		url   string
		depth int
	}
	const maxPages = 30
	seen := map[string]bool{}
	key := func(u string) string { return strings.TrimRight(strings.ToLower(u), "/") }
	var queue []job
	push := func(u string, depth int) {
		if k := key(u); !seen[k] && len(seen) < maxPages*3 {
			seen[k] = true
			queue = append(queue, job{u, depth})
		}
	}
	push(site, 0)
	for _, p := range extra {
		if p = normalizeSite(p); p != "" {
			push(p, 1) // a page the user named: read it and the listing pages it leads to
			home2 := baseDomain(hostOf(p))
			prev := allowed
			allowed = func(u string) bool { return prev(u) || baseDomain(hostOf(u)) == home2 }
		}
	}

	docs := map[string]SiteDoc{}
	var mu sync.Mutex
	read := 0
	for len(queue) > 0 && read < maxPages && ctx.Err() == nil {
		batch := queue
		if len(batch) > 4 {
			batch = batch[:4]
		}
		if read+len(batch) > maxPages {
			batch = batch[:maxPages-read]
		}
		queue = queue[len(batch):]
		read += len(batch)
		var wg sync.WaitGroup
		var next []job
		for _, j := range batch {
			wg.Add(1)
			go func(j job) {
				defer wg.Done()
				body, hdr, final, err := s.get(ctx, j.url, 4<<20)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if j.depth == 0 {
						out.Problems = append(out.Problems, "Couldn't read "+j.url+": "+err.Error())
					}
					return
				}
				if ct := hdr.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") {
					return
				}
				out.Pages = append(out.Pages, final)
				name, links := parsePage(body, final)
				for _, l := range links {
					u, _ := url.Parse(l.href)
					if u == nil {
						continue
					}
					if isDocURL(u) {
						if !allowed(l.href) { // only what the company hosts itself
							continue
						}
						if d, keep := describe(l, final, name); keep {
							if old, dup := docs[key(d.URL)]; !dup || len(d.Title) > len(old.Title) || (old.Date == "" && d.Date != "") {
								if dup && d.Date == "" {
									d.Date = old.Date
								}
								docs[key(d.URL)] = d
							}
						}
						continue
					}
					if !allowed(l.href) || j.depth >= 2 {
						continue
					}
					probe := l.text + " " + u.Path
					if reSkipPage.MatchString(u.Path) || reSkipPage.MatchString(l.text) {
						continue
					}
					if (j.depth == 0 && reWantPage.MatchString(probe)) || (j.depth == 1 && (reStrongPage.MatchString(probe) || u.Query().Get("page") != "")) {
						next = append(next, job{l.href, j.depth + 1})
					}
				}
			}(j)
		}
		wg.Wait()
		// The most promising pages first, so the page limit is spent well.
		sort.SliceStable(next, func(a, b int) bool {
			return reStrongPage.MatchString(next[a].url) && !reStrongPage.MatchString(next[b].url)
		})
		for _, n := range next {
			push(n.url, n.depth)
		}
	}
	if ctx.Err() != nil {
		out.Problems = append(out.Problems, "The scan ran out of time, so some pages were not read.")
	}

	// Files without an extension: ask the server what they are, and drop web pages.
	list := make([]SiteDoc, 0, len(docs))
	for _, d := range docs {
		list = append(list, d)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	asked := 0
	for i := range list {
		if list[i].FileType != "" || asked >= 40 {
			continue
		}
		asked++
		wg.Add(1)
		go func(d *SiteDoc) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(hctx, "HEAD", d.URL, nil)
			req.Header.Set("User-Agent", siteUA)
			resp, err := s.hc.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			ct := resp.Header.Get("Content-Type")
			if strings.Contains(ct, "html") {
				d.Kind = ""
				return
			}
			d.FileType = fileType(d.URL, ct)
			if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" && d.FileType == "" {
				d.FileType = fileType("/"+params["filename"], ct)
			}
		}(&list[i])
	}
	wg.Wait()
	for _, d := range list {
		if d.Kind != "" {
			out.Docs = append(out.Docs, d)
		}
	}
	sort.SliceStable(out.Docs, func(a, b int) bool {
		da, db := out.Docs[a].Date, out.Docs[b].Date
		if da != db {
			return da > db // newest first; undated last
		}
		return out.Docs[a].Title < out.Docs[b].Title
	})
	sort.Strings(out.Pages)
	if len(out.Docs) == 0 && len(out.Problems) == 0 {
		out.Problems = append(out.Problems, "No documents were found on the pages read. The site may build its pages with JavaScript or keep its files on pages this scan did not reach; add the page that lists them.")
	}
	return out
}

// ------------------------------------------------------------------ per company

func (s *Sites) cachePath(cik int) string { return filepath.Join(s.dir, fmt.Sprintf("%d.json", cik)) }

// cached returns the cached scan for a company when it is recent and was made with
// the same website and pages.
func (s *Sites) cached(cik int, site string, extra []string) *SiteScan {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.mem[cik]
	if sc == nil && s.dir != "" {
		if b, err := os.ReadFile(s.cachePath(cik)); err == nil {
			var disk SiteScan
			if json.Unmarshal(b, &disk) == nil {
				sc = &disk
				s.mem[cik] = sc
			}
		}
	}
	if sc == nil || sc.Site != site || strings.Join(sc.Extra, "\n") != strings.Join(extra, "\n") {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, sc.Scanned); err != nil || time.Since(t) > 12*time.Hour {
		return nil
	}
	return sc
}

func (s *Sites) remember(cik int, sc *SiteScan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem[cik] = sc
	if s.dir != "" {
		if b, err := json.Marshal(sc); err == nil {
			_ = writeFileAtomic(s.cachePath(cik), b)
		}
	}
}

// Known reports whether a document address came from this company's last scan.
func (s *Sites) Known(cik int, u string) (SiteDoc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc := s.mem[cik]; sc != nil {
		for _, d := range sc.Docs {
			if d.URL == u {
				return d, true
			}
		}
	}
	return SiteDoc{}, false
}

var (
	reSiteInText = regexp.MustCompile(`(?i)(?:https?://|www\.)[a-z0-9][a-z0-9.-]*\.[a-z]{2,}`)
	notCompany   = regexp.MustCompile(`(?i)sec\.gov|businesswire|globenewswire|prnewswire|accesswire|nasdaq|nyse|linkedin|twitter|x\.com|facebook|instagram|youtube|clinicaltrials\.gov|fda\.gov|ema\.europa|nih\.gov|media-server|webcast|summitcast|onstream|zoom\.|google\.|apple\.|microsoft\.|w3\.org|xbrl|fasb\.org|doi\.org|nejm\.org|bit\.ly`)
)

// siteFromText picks the website a press release most often names.
func siteFromText(text string) string {
	count := map[string]int{}
	for _, m := range reSiteInText.FindAllString(text, -1) {
		h := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(m, "https://"), "http://"), "www."))
		h = strings.TrimRight(h, ".")
		if notCompany.MatchString(h) {
			continue
		}
		count[baseDomain(h)]++
	}
	best, n := "", 0
	for h, c := range count {
		if c > n || (c == n && h < best) {
			best, n = h, c
		}
	}
	if best == "" {
		return ""
	}
	return "https://www." + best
}

// findSite works out a company's website: what the user set, else what the SEC has
// on file, else the address its latest press releases give.
func (a *App) findSite(ctx context.Context, cik int) (site, source string) {
	if u := normalizeSite(a.store.Site(cik).URL); u != "" {
		return u, "you"
	}
	co, filings, err := a.sec.Filings(ctx, cik)
	if err != nil {
		return "", ""
	}
	if u := normalizeSite(co.Website); u != "" {
		return u, "SEC"
	}
	tried := 0
	for _, f := range filings {
		if f.Form != "8-K" || tried >= 3 {
			continue
		}
		tried++
		docs, err := a.sec.FilingDocs(ctx, cik, f.Acc)
		if err != nil {
			continue
		}
		for _, d := range docs {
			if !strings.HasPrefix(strings.ToUpper(d.Type), "EX-99") || !strings.Contains(strings.ToLower(d.Name), ".htm") {
				continue
			}
			b, _, err := a.sec.FetchCached(ctx, d.URL, 30*24*time.Hour)
			if err != nil {
				continue
			}
			if u := siteFromText(reSiteTag.ReplaceAllString(string(b), " ")); u != "" {
				return u, "press release"
			}
		}
	}
	return "", ""
}

func (a *App) getSite(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	site, source := a.findSite(r.Context(), cik)
	extra := a.store.Site(cik).Pages
	if extra == nil {
		extra = []string{}
	}
	if site == "" && len(extra) > 0 {
		site = extra[0]
	}
	if site == "" {
		return &SiteScan{Docs: []SiteDoc{}, Pages: []string{}, Extra: extra, Problems: []string{}}, nil
	}
	sc := a.sites.cached(cik, site, extra)
	if sc == nil || r.URL.Query().Get("fresh") == "1" {
		sc = a.sites.Scan(r.Context(), site, extra)
		a.sites.remember(cik, sc)
	}
	sc.Source = source
	links := make([]string, 0, len(sc.Docs)*2+1)
	links = append(links, sc.Site)
	for _, d := range sc.Docs {
		links = append(links, d.URL, d.Page)
	}
	a.market.allowLinks(links...)
	return sc, nil
}

// putSite sets a company's website, or adds or removes a page to read.
func (a *App) putSite(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	var in struct {
		URL    *string `json:"url"`
		Add    string  `json:"add"`
		Remove string  `json:"remove"`
	}
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	c := a.store.Site(cik)
	if in.URL != nil {
		u := normalizeSite(*in.URL)
		if strings.TrimSpace(*in.URL) != "" && u == "" {
			return nil, bad("That doesn't look like a website address.")
		}
		c.URL = u
	}
	if in.Add != "" {
		u := normalizeSite(in.Add)
		if u == "" {
			return nil, bad("That doesn't look like a web page address.")
		}
		if len(c.Pages) >= 10 {
			return nil, bad("Up to 10 extra pages can be added.")
		}
		dup := false
		for _, p := range c.Pages {
			dup = dup || p == u
		}
		if !dup {
			c.Pages = append(c.Pages, u)
		}
	}
	if in.Remove != "" {
		keep := c.Pages[:0]
		for _, p := range c.Pages {
			if p != in.Remove {
				keep = append(keep, p)
			}
		}
		c.Pages = keep
	}
	if err := a.store.SetSite(cik, c); err != nil {
		return nil, &httpErr{500, err.Error()}
	}
	return ok, nil
}

// saveSiteDoc downloads one listed document into the download folder.
func (a *App) saveSiteDoc(r *http.Request) (any, error) {
	cik, err := pathCIK(r)
	if err != nil {
		return nil, err
	}
	var in struct{ URL, Ticker string }
	if err := readBody(r, &in); err != nil {
		return nil, err
	}
	d, known := a.sites.Known(cik, in.URL)
	if !known {
		return nil, bad("Only documents listed on this tab can be saved.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	b, hdr, _, err := a.sites.getWith(&http.Client{}, ctx, d.URL, 150<<20) // big files need longer than a page does
	if err != nil {
		return nil, &httpErr{502, "Couldn't download that file: " + err.Error()}
	}
	ext := strings.ToLower(d.FileType)
	if ext == "" {
		ext = strings.ToLower(fileType(d.URL, hdr.Get("Content-Type")))
	}
	if ext == "" {
		ext = "pdf"
	}
	tk := sanitizeName(strings.ToUpper(strings.TrimSpace(in.Ticker)))
	if tk == "" {
		tk = fmt.Sprintf("CIK%d", cik)
	}
	title := d.Title
	if r := []rune(title); len(r) > 110 {
		title = string(r[:110])
	}
	name := sanitizeName(strings.TrimSpace(tk + " " + d.Kind + " " + d.Date + " " + title))
	dir := filepath.Join(a.store.DownloadDir(), tk, "Website")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &httpErr{500, "Couldn't create " + dir}
	}
	file := filepath.Join(dir, name+"."+ext)
	if err := writeFileAtomic(file, b); err != nil {
		return nil, &httpErr{500, "Couldn't write " + file}
	}
	return map[string]any{"file": file}, nil
}
