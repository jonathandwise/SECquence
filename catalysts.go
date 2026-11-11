package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Upcoming catalysts, from three sources:
//  1. Company guidance: forward-looking sentences that name a timeframe ("expect to
//     report topline data in the second half of 2027"), taken from the latest
//     10-K/10-Q and recent press releases filed on 8-Ks.
//  2. ClinicalTrials.gov: primary completion dates of the company's studies.
//  3. Estimates: the next periodic report and annual meeting, from last year's dates.

type Period struct {
	Label  string `json:"label"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Phrase string `json:"phrase"` // the words in the sentence that gave the timeframe
	Exact  bool   `json:"exact,omitempty"`
}

type DocRef struct {
	Form  string `json:"form"`
	Filed string `json:"filed"`
	Acc   string `json:"acc"`
	URL   string `json:"url"`
	Doc   string `json:"doc"` // "10-Q", "EX-99.1", ...
	Title string `json:"title"`
}

type Guidance struct {
	Text     string   `json:"text"`
	Kind     string   `json:"kind"`
	Periods  []Period `json:"periods"`
	Programs []string `json:"programs"`
	Source   DocRef   `json:"source"`
	Also     []DocRef `json:"also,omitempty"`
}

type Trial struct {
	NCT             string   `json:"nct"`
	Title           string   `json:"title"`
	Acronym         string   `json:"acronym,omitempty"`
	Status          string   `json:"status"`
	Phases          []string `json:"phases"`
	Enrollment      int      `json:"enrollment"`
	EnrollmentType  string   `json:"enrollmentType,omitempty"`
	Conditions      []string `json:"conditions"`
	Interventions   []string `json:"interventions"`
	Start           string   `json:"start,omitempty"`
	PrimaryComplete string   `json:"primaryCompletion,omitempty"`
	PrimaryType     string   `json:"primaryCompletionType,omitempty"`
	Complete        string   `json:"completion,omitempty"`
	CompleteType    string   `json:"completionType,omitempty"`
	Sponsor         string   `json:"sponsor"`
	LastUpdate      string   `json:"lastUpdate,omitempty"`
	URL             string   `json:"url"`
}

type EstEvent struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Date     string `json:"date"`
	Basis    string `json:"basis"`
	BasisAcc string `json:"basisAcc,omitempty"`
}

type Catalysts struct {
	Guidance []Guidance `json:"guidance"`
	Trials   []Trial    `json:"trials"`
	Events   []EstEvent `json:"events"`
	Scanned  []DocRef   `json:"scanned"`
	Problems []string   `json:"problems"`
	Today    string     `json:"today"`
}

// ---------------------------------------------------------------- text

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	reBlockTag    = regexp.MustCompile(`(?i)</?(p|div|br|tr|li|h[1-6]|table|ul|ol|section|article|center)\b[^>]*>`)
	reCellTag     = regexp.MustCompile(`(?i)</?(td|th)\b[^>]*>`)
	reAnyTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces      = regexp.MustCompile(`[ \t\x{00a0}\x{2009}\x{202f}]+`)
)

// htmlToParagraphs turns a filing into plain-text paragraphs. Table cells are joined
// with " | " so table rows can be told apart from prose.
func htmlToParagraphs(doc string) []string {
	doc = reIxHeader.ReplaceAllString(doc, " ")
	doc = reScriptStyle.ReplaceAllString(doc, " ")
	doc = reBlockTag.ReplaceAllString(doc, "\n")
	doc = reCellTag.ReplaceAllString(doc, " | ")
	doc = html.UnescapeString(reAnyTag.ReplaceAllString(doc, ""))
	var out []string
	for _, p := range strings.Split(doc, "\n") {
		if p = strings.TrimSpace(reSpaces.ReplaceAllString(p, " ")); len(p) >= 30 {
			out = append(out, p)
		}
	}
	return out
}

var abbreviations = map[string]bool{"inc": true, "corp": true, "co": true, "ltd": true, "no": true, "nos": true, "dr": true, "mr": true, "ms": true,
	"mrs": true, "vs": true, "e.g": true, "i.e": true, "approx": true, "u.s": true, "jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true, "st": true, "etc": true, "fig": true, "al": true,
	"ph.d": true, "m.d": true}

// splitSentences breaks a paragraph at ". " followed by a capital, except after
// abbreviations ("Inc.", "U.S.") and initials.
func splitSentences(p string) []string {
	rs := []rune(p)
	var out []string
	start := 0
	for i := 0; i < len(rs)-2; i++ {
		if c := rs[i]; (c != '.' && c != '!' && c != '?') || rs[i+1] != ' ' {
			continue
		}
		if n := rs[i+2]; !(n >= 'A' && n <= 'Z') && n != '“' && n != '"' && n != '(' {
			continue
		}
		j := i - 1
		for j >= start && rs[j] != ' ' {
			j--
		}
		word := strings.ToLower(strings.Trim(string(rs[j+1:i]), `("“`))
		if abbreviations[word] || (len([]rune(word)) == 1 && rs[i] == '.') {
			continue
		}
		out = append(out, strings.TrimSpace(string(rs[start:i+1])))
		start = i + 2
	}
	if start < len(rs) {
		out = append(out, strings.TrimSpace(string(rs[start:])))
	}
	return out
}

// ---------------------------------------------------------------- timeframes

const months = `(January|February|March|April|May|June|July|August|September|October|November|December)`

var monthNum = map[string]int{"january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6, "july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12}

func ymd(y, m, d int) string { return fmt.Sprintf("%04d-%02d-%02d", y, m, d) }
func monthEnd(y, m int) string {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}
func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func ordinal(s string) int {
	switch strings.ToLower(s) {
	case "first", "1st", "1":
		return 1
	case "second", "2nd", "2":
		return 2
	case "third", "3rd", "3":
		return 3
	case "fourth", "4th", "4":
		return 4
	}
	return 0
}

func wholeYear(y int) Period {
	return Period{Label: strconv.Itoa(y), Start: ymd(y, 1, 1), End: ymd(y, 12, 31)}
}

func halfYear(h, y int) Period {
	if h == 1 {
		return Period{Label: fmt.Sprintf("1H %d", y), Start: ymd(y, 1, 1), End: ymd(y, 6, 30)}
	}
	return Period{Label: fmt.Sprintf("2H %d", y), Start: ymd(y, 7, 1), End: ymd(y, 12, 31)}
}

func quarterOf(q, y int) Period {
	m := 3*(q-1) + 1
	return Period{Label: fmt.Sprintf("Q%d %d", q, y), Start: ymd(y, m, 1), End: monthEnd(y, m+2)}
}

// partOfYear handles "early", "mid", "late" and "end of" a year.
func partOfYear(word string, y int) (Period, bool) {
	switch w := strings.ToLower(strings.TrimSpace(word)); {
	case strings.HasPrefix(w, "mid"):
		return Period{Label: fmt.Sprintf("Mid-%d", y), Start: ymd(y, 5, 1), End: ymd(y, 8, 31)}, true
	case strings.HasPrefix(w, "early"), strings.Contains(w, "beginning"), strings.Contains(w, "start"):
		return Period{Label: fmt.Sprintf("Early %d", y), Start: ymd(y, 1, 1), End: ymd(y, 4, 30)}, true
	case strings.HasPrefix(w, "late"):
		return Period{Label: fmt.Sprintf("Late %d", y), Start: ymd(y, 9, 1), End: ymd(y, 12, 31)}, true
	case strings.Contains(w, "end"):
		return Period{Label: fmt.Sprintf("YE %d", y), Start: ymd(y, 10, 1), End: ymd(y, 12, 31)}, true
	}
	return Period{}, false
}

// Timeframe phrases, most specific first: once a phrase matches, later rules skip
// that stretch of the sentence. docYear resolves "this year" and "next year".
var periodRules = []struct {
	re   *regexp.Regexp
	conv func(m []string, docYear int) (Period, bool)
}{
	{regexp.MustCompile(`(?i)\b(first|second|1st|2nd)\s+half\s+(?:of\s+)?(?:calendar\s+|fiscal\s+)?(?:year\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) { return halfYear(ordinal(m[1]), atoi(m[2])), true }},
	{regexp.MustCompile(`(?i)\b(?:([12])H|H([12]))[\s']*(20\d\d|\d\d)\b`),
		func(m []string, _ int) (Period, bool) {
			y := atoi(m[3])
			if y < 100 {
				y += 2000
			}
			return halfYear(ordinal(m[1]+m[2]), y), true
		}},
	{regexp.MustCompile(`(?i)\b(first|second|third|fourth|1st|2nd|3rd|4th)\s+(?:calendar\s+|fiscal\s+)?quarter\s+(?:of\s+)?(?:calendar\s+|fiscal\s+)?(?:year\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) { return quarterOf(ordinal(m[1]), atoi(m[2])), true }},
	{regexp.MustCompile(`(?i)\bQ([1-4])\s*(?:of\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) { return quarterOf(ordinal(m[1]), atoi(m[2])), true }},
	{regexp.MustCompile(`(?i)\b` + months + `\s+(\d{1,2}),?\s+(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) {
			d := atoi(m[2])
			if d < 1 || d > 31 {
				return Period{}, false
			}
			t := time.Date(atoi(m[3]), time.Month(monthNum[strings.ToLower(m[1])]), d, 0, 0, 0, 0, time.UTC)
			day := t.Format("2006-01-02")
			return Period{Label: t.Format("Jan 2, 2006"), Start: day, End: day, Exact: true}, true
		}},
	{regexp.MustCompile(`(?i)\b(mid|early|late|year[- ]end|the\s+end\s+of|end\s+of|the\s+beginning\s+of|beginning\s+of|the\s+start\s+of|start\s+of)[\s-]+(?:of\s+)?(?:calendar\s+|fiscal\s+)?(?:year\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) { return partOfYear(m[1], atoi(m[2])) }},
	{regexp.MustCompile(`(?i)\b` + months + `\s+(?:of\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) {
			mo, y := monthNum[strings.ToLower(m[1])], atoi(m[2])
			label := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Format("Jan 2006")
			return Period{Label: label, Start: ymd(y, mo, 1), End: monthEnd(y, mo)}, true
		}},
	{regexp.MustCompile(`(?i)\b(later\s+this\s+year|by\s+(?:the\s+)?end\s+of\s+(?:the|this)\s+year|by\s+year[- ]end|before\s+year[- ]end|this\s+year)\b`),
		func(m []string, docYear int) (Period, bool) {
			if strings.Contains(strings.ToLower(m[1]), "end") {
				return partOfYear("end", docYear)
			}
			return wholeYear(docYear), true
		}},
	{regexp.MustCompile(`(?i)\b(early|mid|late)?[\s-]*next\s+year\b`),
		func(m []string, docYear int) (Period, bool) {
			if m[1] != "" {
				return partOfYear(m[1], docYear+1)
			}
			return wholeYear(docYear + 1), true
		}},
	{regexp.MustCompile(`(?i)\b(?:in|by|during|throughout|within|for|into|through)\s+(?:calendar\s+|fiscal\s+)?(?:year\s+)?(20\d\d)\b`),
		func(m []string, _ int) (Period, bool) { return wholeYear(atoi(m[1])), true }},
}

// findPeriods returns the timeframes a sentence mentions, in sentence order.
func findPeriods(s string, docYear int) []Period {
	type hit struct {
		from, to int
		p        Period
	}
	var hits []hit
	overlaps := func(a, b int) bool {
		for _, h := range hits {
			if a < h.to && b > h.from {
				return true
			}
		}
		return false
	}
	for _, rule := range periodRules {
		for _, loc := range rule.re.FindAllStringSubmatchIndex(s, -1) {
			if overlaps(loc[0], loc[1]) {
				continue
			}
			m := make([]string, len(loc)/2)
			for i := range m {
				if loc[2*i] >= 0 {
					m[i] = s[loc[2*i]:loc[2*i+1]]
				}
			}
			if p, ok := rule.conv(m, docYear); ok && p.Start >= "2000" && p.Start < "2100" {
				p.Phrase = strings.TrimSpace(m[0])
				hits = append(hits, hit{loc[0], loc[1], p})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].from < hits[j].from })
	out := make([]Period, len(hits))
	for i, h := range hits {
		out[i] = h.p
	}
	return out
}

// ---------------------------------------------------------------- guidance

var (
	// A guidance sentence looks ahead, is about a catalyst, and is not boilerplate.
	reForward  = regexp.MustCompile(`(?i)\b(expects?|expected|anticipates?|anticipated|plans?|planned|intends?|on track|guidance|estimates?|scheduled|targets?|targeting|believes?|aims?|look(?:s|ing)? forward|will (?:report|present|initiate|submit|announce|complete|begin|start|file|share|provide|host|dose|enroll|commence|seek|request|meet))\b`)
	reCatalyst = regexp.MustCompile(`(?i)\b(data|results?|readouts?|read-out|top-?line|initiat\w*|enroll\w*|first patient|dos(?:e|ed|ing)|submi\w*|filing|NDA|BLA|MAA|IND|CTA|approval|PDUFA|launch\w*|commerciali\w*|FDA|EMA|PMDA|end-of-phase|Phase\s*[1-4I]|study|studies|trial|registrational|pivotal|runway|fund (?:our |its )?operat\w*|milestone|meeting|regulatory|label|interim|cohort|open-label extension|OLE)\b`)
	reBoiler   = regexp.MustCompile(`(?i)(forward-looking|safe harbor|private securities litigation|undue reliance|could cause actual results|no obligation to|actual results may differ|risk factors|we cannot assure|there can be no assurance|unaudited|in thousands|accompanying notes|incorporated (?:herein )?by reference|exhibit \d)`)

	kinds = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"Regulatory", regexp.MustCompile(`(?i)\b(PDUFA|NDA|BLA|MAA|marketing authori[sz]ation|approval|regulatory (?:submission|filing|path|feedback|meeting)|submit\w*|end-of-phase|breakthrough|FDA|EMA|PMDA|IND|CTA)\b`)},
		{"Data readout", regexp.MustCompile(`(?i)\b(data|results?|readouts?|read-out|top-?line|interim analysis)\b`)},
		{"Trial start / enrollment", regexp.MustCompile(`(?i)\b(initiat\w*|enroll\w*|first patient|dos(?:e|ed|ing)|commenc\w*|begin|start)\b`)},
		{"Commercial", regexp.MustCompile(`(?i)\b(launch\w*|commerciali\w*)\b`)},
		{"Cash runway", regexp.MustCompile(`(?i)\b(runway|fund (?:our |its )?operat\w*|sufficient to fund|cash, cash equivalents)\b`)},
	}

	// Drug names: code names like STK-001, and generic names by their stems.
	reCodeName = regexp.MustCompile(`\b([A-Z]{2,5}-\d{2,5}[A-Z]?)\b`)
	reINN      = regexp.MustCompile(`\b([a-z]{5,}(?:nersen|rsen|isen|mab|tinib|ciclib|parib|lisib|glutide|leucel|gene\s?\w*vec))\b`)
	notDrugs   = map[string]bool{"EX": true, "FY": true, "CY": true, "US": true, "SEC": true, "COVID": true, "IRS": true, "ASU": true, "ASC": true, "PH": true}
)

// programsIn lists the drug names in a sentence, in the order they appear.
func programsIn(s string) []string {
	type hit struct {
		pos  int
		name string
	}
	var hits []hit
	seen := map[string]bool{}
	for _, loc := range reCodeName.FindAllStringSubmatchIndex(s, -1) {
		name := s[loc[2]:loc[3]]
		if prefix := name[:strings.Index(name, "-")]; !notDrugs[prefix] && !seen[name] {
			seen[name] = true
			hits = append(hits, hit{loc[0], name})
		}
	}
	for _, loc := range reINN.FindAllStringSubmatchIndex(s, -1) {
		if name := strings.ToLower(s[loc[2]:loc[3]]); !seen[name] {
			seen[name] = true
			hits = append(hits, hit{loc[0], name})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

func classify(s string) string {
	for _, k := range kinds {
		if k.re.MatchString(s) {
			return k.name
		}
	}
	return "Milestone"
}

// extractGuidance finds the guidance sentences in one document.
func extractGuidance(doc string, src DocRef, docYear int) []Guidance {
	var out []Guidance
	for _, p := range htmlToParagraphs(doc) {
		if strings.Count(p, "|") > 3 { // a table row
			continue
		}
		for _, s := range splitSentences(p) {
			if len(s) < 40 || len(s) > 700 || reBoiler.MatchString(s) || !reForward.MatchString(s) || !reCatalyst.MatchString(s) {
				continue
			}
			if periods := findPeriods(s, docYear); len(periods) > 0 {
				out = append(out, Guidance{Text: s, Kind: classify(s), Periods: periods, Programs: programsIn(s), Source: src})
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- ClinicalTrials.gov

type ctDate struct {
	Date string `json:"date"`
	Type string `json:"type"`
}

// parseCTGov reads one page of studies, keeping those the sponsor leads or
// collaborates on (the search itself also matches loosely similar names).
func parseCTGov(body []byte, sponsor string) ([]Trial, string, error) {
	var r struct {
		Studies []struct {
			P struct {
				ID struct {
					NCT     string `json:"nctId"`
					Title   string `json:"briefTitle"`
					Acronym string `json:"acronym"`
				} `json:"identificationModule"`
				Status struct {
					Overall    string `json:"overallStatus"`
					Start      ctDate `json:"startDateStruct"`
					Primary    ctDate `json:"primaryCompletionDateStruct"`
					Completion ctDate `json:"completionDateStruct"`
					Updated    ctDate `json:"lastUpdatePostDateStruct"`
				} `json:"statusModule"`
				Sponsors struct {
					Lead struct {
						Name string `json:"name"`
					} `json:"leadSponsor"`
					Collaborators []struct {
						Name string `json:"name"`
					} `json:"collaborators"`
				} `json:"sponsorCollaboratorsModule"`
				Conditions struct {
					List []string `json:"conditions"`
				} `json:"conditionsModule"`
				Design struct {
					Phases     []string `json:"phases"`
					Enrollment struct {
						Count int    `json:"count"`
						Type  string `json:"type"`
					} `json:"enrollmentInfo"`
				} `json:"designModule"`
				Arms struct {
					Interventions []struct {
						Name string `json:"name"`
					} `json:"interventions"`
				} `json:"armsInterventionsModule"`
			} `json:"protocolSection"`
		} `json:"studies"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, "", fmt.Errorf("unexpected response from ClinicalTrials.gov")
	}
	want := strings.ToLower(sponsor)
	var out []Trial
	for _, st := range r.Studies {
		p := st.P
		ours := strings.Contains(strings.ToLower(p.Sponsors.Lead.Name), want)
		for _, c := range p.Sponsors.Collaborators {
			ours = ours || strings.Contains(strings.ToLower(c.Name), want)
		}
		if !ours || p.ID.NCT == "" {
			continue
		}
		t := Trial{NCT: p.ID.NCT, Title: p.ID.Title, Acronym: p.ID.Acronym, Status: p.Status.Overall, Phases: p.Design.Phases,
			Enrollment: p.Design.Enrollment.Count, EnrollmentType: p.Design.Enrollment.Type, Conditions: p.Conditions.List,
			Start: p.Status.Start.Date, PrimaryComplete: p.Status.Primary.Date, PrimaryType: p.Status.Primary.Type,
			Complete: p.Status.Completion.Date, CompleteType: p.Status.Completion.Type, Sponsor: p.Sponsors.Lead.Name,
			LastUpdate: p.Status.Updated.Date, URL: "https://clinicaltrials.gov/study/" + p.ID.NCT, Interventions: []string{}}
		for _, iv := range p.Arms.Interventions {
			if !strings.Contains(strings.ToLower(iv.Name), "placebo") {
				t.Interventions = append(t.Interventions, iv.Name)
			}
		}
		if t.Phases == nil {
			t.Phases = []string{}
		}
		if t.Conditions == nil {
			t.Conditions = []string{}
		}
		out = append(out, t)
	}
	return out, r.NextPageToken, nil
}

// Trials lists a sponsor's studies on ClinicalTrials.gov.
func (m *Market) Trials(ctx context.Context, sponsor string) ([]Trial, error) {
	all := []Trial{}
	token := ""
	for page := 0; page < 4; page++ {
		u := fmt.Sprintf("%s/api/v2/studies?query.spons=%s&pageSize=100&format=json", strings.TrimRight(m.CTGov, "/"), url.QueryEscape(`"`+sponsor+`"`))
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		body, err := m.get(ctx, u, 6*time.Hour)
		if err != nil {
			return all, fmt.Errorf("ClinicalTrials.gov couldn't be reached (%v)", err)
		}
		trials, next, err := parseCTGov(body, sponsor)
		if err != nil {
			return all, err
		}
		for _, t := range trials {
			all = append(all, t)
			m.allowLinks(t.URL)
		}
		if token = next; token == "" {
			break
		}
	}
	return all, nil
}

// ---------------------------------------------------------------- estimated events

// estimateEvents projects the next 10-Q/10-K and annual meeting from last year's dates.
func estimateEvents(filings []Filing, today time.Time) []EstEvent {
	out := []EstEvent{}
	day := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	// Next periodic report: the earliest 10-Q/10-K filed in the 150 days after this
	// date last year, moved forward 52 weeks.
	yearAgo := today.AddDate(-1, 0, 0)
	var base *Filing
	for i := range filings {
		f := &filings[i]
		if d := day(f.Filed); (f.Form == "10-Q" || f.Form == "10-K") && d.After(yearAgo.AddDate(0, 0, -3)) && d.Before(yearAgo.AddDate(0, 0, 150)) {
			if base == nil || f.Filed < base.Filed {
				base = f
			}
		}
	}
	if base != nil {
		est := day(base.Filed).AddDate(0, 0, 364)
		filedAlready := false
		for _, f := range filings {
			filedAlready = filedAlready || (f.Form == base.Form && day(f.Filed).After(est.AddDate(0, 0, -45)))
		}
		if !filedAlready && est.After(today.AddDate(0, 0, -1)) {
			title := "Quarterly results and 10-Q"
			if pd, err := time.Parse("2006-01-02", base.Period); err == nil {
				next := pd.AddDate(1, 0, 0)
				title = fmt.Sprintf("Q%d %d results and 10-Q", (int(next.Month())-1)/3+1, next.Year())
				if base.Form == "10-K" {
					title = fmt.Sprintf("FY %d results and 10-K", next.Year())
				}
			}
			out = append(out, EstEvent{Kind: "Earnings", Title: title, Date: est.Format("2006-01-02"),
				Basis: fmt.Sprintf("Estimated from last year's %s, filed %s", base.Form, base.Filed), BasisAcc: base.Acc})
		}
	}
	// Annual meeting: the latest 8-K reporting vote results (item 5.07), plus 52 weeks.
	for _, f := range filings {
		if !strings.HasPrefix(f.Form, "8-K") || !strings.Contains(f.Items, "5.07") {
			continue
		}
		meeting := day(f.Filed)
		if pd, err := time.Parse("2006-01-02", f.Period); err == nil {
			meeting = pd
		}
		if est := meeting.AddDate(0, 0, 364); est.After(today) && meeting.After(today.AddDate(-1, -1, 0)) {
			out = append(out, EstEvent{Kind: "Annual meeting", Title: "Annual shareholder meeting", Date: est.Format("2006-01-02"),
				Basis: "Estimated from last year's meeting (" + meeting.Format("2006-01-02") + ")", BasisAcc: f.Acc})
		}
		break
	}
	return out
}

// ---------------------------------------------------------------- assembly

// guidanceDocs picks the documents worth reading: the latest annual report, a newer
// 10-Q if there is one, the latest earnings release, and recent press releases.
func guidanceDocs(ctx context.Context, sec *SEC, cik int, filings []Filing, today time.Time) []DocRef {
	var docs []DocRef
	var haveAnnual, haveQuarterly, haveEarnings bool
	releases := 0
	for _, f := range filings {
		filed, _ := time.Parse("2006-01-02", f.Filed)
		switch {
		case (f.Form == "10-K" || f.Form == "20-F") && !haveAnnual && filed.After(today.AddDate(-2, 0, 0)):
			haveAnnual = true
			docs = append(docs, DocRef{Form: f.Form, Filed: f.Filed, Acc: f.Acc, URL: f.URL, Doc: f.Form, Title: f.Form + " annual report"})
		case f.Form == "10-Q" && !haveQuarterly && !haveAnnual && filed.After(today.AddDate(-1, 0, 0)):
			haveQuarterly = true
			docs = append(docs, DocRef{Form: f.Form, Filed: f.Filed, Acc: f.Acc, URL: f.URL, Doc: f.Form, Title: "10-Q quarterly report"})
		case strings.HasPrefix(f.Form, "8-K") && filed.After(today.AddDate(0, -15, 0)):
			earnings := strings.Contains(f.Items, "2.02")
			release := (strings.Contains(f.Items, "7.01") || strings.Contains(f.Items, "8.01")) && filed.After(today.AddDate(0, 0, -150))
			if !(earnings && !haveEarnings) && !(release && releases < 4) {
				continue
			}
			exhibits, err := sec.FilingDocs(ctx, cik, f.Acc)
			if err != nil {
				continue
			}
			title := "Press release"
			if earnings {
				haveEarnings, title = true, "Earnings press release"
			} else {
				releases++
			}
			for _, x := range exhibits {
				if x.Kind == "exhibit" && strings.HasPrefix(strings.ToUpper(x.Type), "EX-99") && strings.HasSuffix(strings.ToLower(x.Name), "htm") {
					docs = append(docs, DocRef{Form: f.Form, Filed: f.Filed, Acc: f.Acc, URL: x.URL, Doc: strings.ToUpper(x.Type), Title: title})
				}
			}
		}
		if len(docs) >= 12 {
			break
		}
	}
	return docs
}

// FindCatalysts compiles the catalyst timeline for a company.
func FindCatalysts(ctx context.Context, sec *SEC, market *Market, cik int, name string) (*Catalysts, error) {
	_, filings, err := sec.Filings(ctx, cik)
	if err != nil {
		return nil, err
	}
	today := time.Now()
	res := &Catalysts{Guidance: []Guidance{}, Trials: []Trial{}, Scanned: []DocRef{}, Problems: []string{}, Today: today.Format("2006-01-02")}
	index := map[string]int{} // normalised sentence -> position in res.Guidance
	for _, ref := range guidanceDocs(ctx, sec, cik, filings, today) {
		if ref.URL == "" {
			continue
		}
		body, _, err := sec.FetchCached(ctx, ref.URL, 24*time.Hour)
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("couldn't read %s %s (%s)", ref.Form, ref.Filed, path.Base(ref.URL)))
			continue
		}
		res.Scanned = append(res.Scanned, ref)
		docYear := today.Year()
		if y, err := strconv.Atoi(ref.Filed[:min(4, len(ref.Filed))]); err == nil {
			docYear = y
		}
		for _, g := range extractGuidance(string(body), ref, docYear) {
			key := reNonWord.ReplaceAllString(strings.ToLower(g.Text), "")
			if len(key) > 90 {
				key = key[:90]
			}
			if i, dup := index[key]; dup { // the same statement in an older document
				res.Guidance[i].Also = append(res.Guidance[i].Also, ref)
				continue
			}
			index[key] = len(res.Guidance)
			res.Guidance = append(res.Guidance, g)
		}
	}
	if sponsor := cleanCompanyName(name); sponsor != "" {
		trials, err := market.Trials(ctx, sponsor)
		if err != nil {
			res.Problems = append(res.Problems, err.Error())
		}
		res.Trials = trials
	}
	res.Events = estimateEvents(filings, today)
	return res, nil
}
