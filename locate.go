package main

import (
	"context"
	"fmt"
	"html"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Fact-checking: finding a reported number inside the filing it came from.
//
// Financial statements filed since about 2019-2021 are inline XBRL: each number in the
// HTML sits inside <ix:nonFraction name="us-gaap:Concept" contextRef="...">, and the
// referenced context gives its period. Given a value's concept, period and accession
// number, Locate finds the matching tags (skipping segment breakdowns) and the
// document viewer highlights them.

var (
	reIxOpen   = regexp.MustCompile(`(?is)<ix:nonfraction\b([^>]*)>`)
	reIxClose  = regexp.MustCompile(`(?is)</ix:nonfraction\s*>`)
	reIxHeader = regexp.MustCompile(`(?is)<ix:header\b.*?</ix:header\s*>`)
	reCtx      = regexp.MustCompile(`(?is)<(?:xbrli:)?context\b([^>]*)>(.*?)</(?:xbrli:)?context\s*>`)
	reAttr     = regexp.MustCompile(`([\w:.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reStartD   = regexp.MustCompile(`(?is)<(?:xbrli:)?startdate>\s*([0-9-]+)\s*<`)
	reEndD     = regexp.MustCompile(`(?is)<(?:xbrli:)?enddate>\s*([0-9-]+)\s*<`)
	reInstant  = regexp.MustCompile(`(?is)<(?:xbrli:)?instant>\s*([0-9-]+)\s*<`)
	reSegment  = regexp.MustCompile(`(?is)<(?:xbrli:)?(segment|scenario)\b`)
	reHead     = regexp.MustCompile(`(?i)<head[^>]*>`)
)

type ixCtx struct {
	Start, End, Instant string
	Dim                 bool // has a segment: a breakdown, not the statement total
}

type ixFact struct {
	Index  int // position among the document's <ix:nonFraction> tags
	Name   string
	Ctx    string
	Text   string
	Scale  int
	Sign   string
	Val    float64
	Hidden bool // inside <ix:header>, so not visible on the page
}

func attrs(s string) map[string]string {
	m := map[string]string{}
	for _, a := range reAttr.FindAllStringSubmatch(s, -1) {
		m[strings.ToLower(a[1])] = a[2] + a[3]
	}
	return m
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// ixValue converts displayed text such as "24,512" with scale 3 to 24512000.
func ixValue(text, format string, scale int, sign string) float64 {
	f := strings.ToLower(format)
	commaDecimal := strings.Contains(f, "numcommadecimal") || strings.Contains(f, "num-comma-decimal")
	var b strings.Builder
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case (r == '.' && !commaDecimal) || (r == ',' && commaDecimal):
			b.WriteByte('.')
		}
	}
	v, _ := strconv.ParseFloat(b.String(), 64) // dashes and "none" formats read as zero
	v *= math.Pow10(scale)
	if sign == "-" {
		v = -v
	}
	return v
}

func parseIX(doc string) (map[string]ixCtx, []ixFact) {
	ctxs := map[string]ixCtx{}
	for _, m := range reCtx.FindAllStringSubmatch(doc, -1) {
		if id := attrs(m[1])["id"]; id != "" {
			ctxs[id] = ixCtx{Start: first(reStartD, m[2]), End: first(reEndD, m[2]), Instant: first(reInstant, m[2]), Dim: reSegment.MatchString(m[2])}
		}
	}
	headers := reIxHeader.FindAllStringIndex(doc, -1)
	var facts []ixFact
	for i, loc := range reIxOpen.FindAllStringSubmatchIndex(doc, -1) {
		a := attrs(doc[loc[2]:loc[3]])
		f := ixFact{Index: i, Name: a["name"], Ctx: a["contextref"], Sign: a["sign"]}
		f.Scale, _ = strconv.Atoi(a["scale"])
		if end := reIxClose.FindStringIndex(doc[loc[1]:]); end != nil {
			f.Text = cellText(doc[loc[1] : loc[1]+end[0]])
		}
		f.Val = ixValue(f.Text, a["format"], f.Scale, f.Sign)
		for _, h := range headers {
			f.Hidden = f.Hidden || (loc[0] >= h[0] && loc[0] < h[1])
		}
		facts = append(facts, f)
	}
	return ctxs, facts
}

func localName(n string) string { return n[strings.LastIndex(n, ":")+1:] }

// matches reports whether a value shown in the document agrees with the SEC's data,
// allowing for the document's rounding.
func (f ixFact) matches(val float64) bool {
	return math.Abs(f.Val-val) <= 0.5*math.Pow10(f.Scale)+1
}

// findInDoc returns the tags for a concept and period: visible ones first, then those
// whose value matches, in document order. inline reports whether the document is
// tagged at all.
func findInDoc(doc, concept, start, end string, val float64) (hits []ixFact, inline bool) {
	ctxs, facts := parseIX(doc)
	want := localName(concept)
	for _, f := range facts {
		c, ok := ctxs[f.Ctx]
		if !ok || c.Dim || !strings.EqualFold(localName(f.Name), want) {
			continue
		}
		if (start == "" && c.Instant == end) || (start != "" && c.Start == start && c.End == end) {
			hits = append(hits, f)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Hidden != b.Hidden {
			return !a.Hidden
		}
		if a.matches(val) != b.matches(val) {
			return a.matches(val)
		}
		return a.Index < b.Index
	})
	return hits, len(facts) > 0
}

type LocateResult struct {
	URL         string  `json:"url"`
	DocName     string  `json:"docName"`
	Hits        []int   `json:"hits"`
	Shown       string  `json:"shown"`
	Scale       int     `json:"scale"`
	Sign        string  `json:"sign"`
	DocValue    float64 `json:"docValue"`
	ValueMatch  bool    `json:"valueMatch"`
	Inline      bool    `json:"inline"`
	Message     string  `json:"message,omitempty"`
	FilingIndex string  `json:"filingIndex"`
}

func (s *SEC) Locate(ctx context.Context, cik int, accn, concept, start, end string, val float64) (*LocateResult, error) {
	if !reAcc.MatchString(accn) {
		return nil, fmt.Errorf("invalid accession number %q", accn)
	}
	res := &LocateResult{Hits: []int{}, FilingIndex: s.archiveBase(cik, accn) + "/" + accn + "-index.htm"}
	// Candidates: the filing's primary document, then its other HTML documents.
	var cands []string
	add := func(u string) {
		for _, c := range cands {
			if c == u {
				return
			}
		}
		cands = append(cands, u)
	}
	if _, filings, err := s.Filings(ctx, cik); err == nil {
		for _, f := range filings {
			if f.Acc == accn && f.URL != "" {
				add(f.URL)
			}
		}
	}
	if docs, err := s.FilingDocs(ctx, cik, accn); err == nil {
		for _, d := range docs {
			ext := strings.ToLower(path.Ext(d.Name))
			if (d.Kind == "main" || d.Kind == "other" || d.Kind == "exhibit") && (ext == ".htm" || ext == ".html") {
				add(d.URL)
			}
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("couldn't find the documents for filing %s", accn)
	}
	anyInline := false
	for _, u := range cands[:min(len(cands), 6)] {
		body, _, err := s.FetchCached(ctx, u, 24*time.Hour)
		if err != nil {
			continue
		}
		hits, inline := findInDoc(string(body), concept, start, end, val)
		anyInline = anyInline || inline
		if len(hits) == 0 {
			continue
		}
		h := hits[0]
		res.URL, res.DocName, res.Inline = u, path.Base(u), true
		for _, x := range hits {
			res.Hits = append(res.Hits, x.Index)
		}
		res.Shown, res.Scale, res.Sign, res.DocValue, res.ValueMatch = h.Text, h.Scale, h.Sign, h.Val, h.matches(val)
		if h.Hidden {
			res.Message = "This value is tagged in the filing's hidden XBRL data rather than shown in a table."
		}
		return res, nil
	}
	res.URL, res.DocName, res.Inline = cands[0], path.Base(cands[0]), anyInline
	if anyInline {
		res.Message = "The filing is tagged, but this exact value wasn't found in its documents. It may appear only in an exhibit or the XBRL data. The main document is shown."
	} else {
		res.Message = "This filing was made before inline XBRL tagging, so the number can't be highlighted automatically. The document is shown; look in its financial statements."
	}
	return res, nil
}

const hitStyle = `<style>[data-secq-hit]{background:#ffe066!important;color:#000!important;box-shadow:0 0 0 3px #ffe066;border-radius:2px}` +
	`#secq-hit{background:#ffa94d!important;box-shadow:0 0 0 3px #ffa94d,0 0 0 6px rgba(255,146,43,.35);scroll-margin-top:35vh}</style>`

// insertAfterHead puts markup right after <head>, or at the start if there is none.
func insertAfterHead(body []byte, markup string) []byte {
	at := 0
	if loc := reHead.FindIndex(body[:min(len(body), 20000)]); loc != nil {
		at = loc[1]
	}
	out := make([]byte, 0, len(body)+len(markup))
	out = append(out, body[:at]...)
	out = append(out, markup...)
	return append(out, body[at:]...)
}

// highlightIX marks the given <ix:nonFraction> tags (by position) so the viewer can
// show them; the first one gets id="secq-hit" as the scroll target.
func highlightIX(body []byte, hits []int) []byte {
	if len(hits) == 0 {
		return body
	}
	mark := map[int]string{}
	for i, h := range hits {
		if _, ok := mark[h]; !ok {
			mark[h] = ` data-secq-hit="1"`
			if i == 0 {
				mark[h] = ` id="secq-hit" data-secq-hit="1"`
			}
		}
	}
	var out []byte
	prev := 0
	for i, loc := range reIxOpen.FindAllIndex(body, -1) {
		if ins, ok := mark[i]; ok {
			nameEnd := loc[0] + len("<ix:nonFraction")
			out = append(out, body[prev:nameEnd]...)
			out = append(out, ins...)
			prev = nameEnd
		}
	}
	out = append(out, body[prev:]...)
	return insertAfterHead(out, hitStyle)
}

// injectBase makes a proxied document resolve its images and links against sec.gov.
func injectBase(body []byte, dir string) []byte {
	return insertAfterHead(body, `<base href="`+htmlEscape(dir)+`" target="_blank">`)
}

var reAnchorHref = regexp.MustCompile(`(?is)<a\b([^>]*?\bhref\s*=\s*)("[^"]*"|'[^']*'|[^\s"'>]+)`)

// keepJumpsInDoc rewrites links that point inside the same document (a table of
// contents entry such as href="#item1" or href="doc.htm#item1") so they scroll the
// viewer instead of following <base> out to sec.gov. self is the absolute URL the
// viewer loaded the document from; docName is the document's file name.
func keepJumpsInDoc(body []byte, self, docName string) []byte {
	if i := strings.IndexByte(self, '#'); i >= 0 {
		self = self[:i]
	}
	return reAnchorHref.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := reAnchorHref.FindSubmatch(m)
		raw := string(sub[2])
		if raw[0] == '"' || raw[0] == '\'' {
			raw = raw[1 : len(raw)-1]
		}
		href := html.UnescapeString(raw)
		i := strings.IndexByte(href, '#')
		if i < 0 {
			return m
		}
		if before := strings.TrimPrefix(href[:i], "./"); before != "" && !strings.EqualFold(before, docName) {
			return m
		}
		return []byte(`<a target="_self"` + string(sub[1]) + `"` + htmlEscape(self+href[i:]) + `"`)
	})
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}
