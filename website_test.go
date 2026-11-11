package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A small company website shaped like the real ones: an investor section whose
// files have no extension, and a publications page whose links say "View poster".
func mockSite() *httptest.Server {
	pages := map[string]string{
		"/": `<html><head><title>Acme Bio</title></head><body><nav>
			<a href="/science/publications/">Publications</a> <a href="/investors/">Investors &amp; News</a>
			<a href="/careers/">Careers</a> <a href="/privacy-statement/">Privacy</a> <a href="https://www.linkedin.com/company/acme">LinkedIn</a></nav>
			<script>var x = '<a href="/hidden.pdf">no</a>';</script></body></html>`,
		"/investors/": `<html><head><title>Investors | Acme Bio</title></head><body>
			<a href="/investors/events-and-presentations">Events &amp; Presentations</a> <a href="/investors/press-releases">Press Releases</a>
			<a href="/investors/governance">Governance</a> <a href="/investors/financials-and-sec-filings">SEC Filings</a>
			<h3>Featured</h3><a href="/static-files/q3deck">Acme Third Quarter 2026 Business Update</a></body></html>`,
		"/investors/events-and-presentations": `<html><head><title>Events &amp; Presentations | Acme Bio</title></head><body>
			<div><span>October 29, 2026 at 4:30 PM EDT</span><a href="/events/event-details/q3">Acme Third Quarter 2026 Business and Financial Update</a>
			<a href="https://edge.media-server.com/mmc/p/x">Click here for webcast</a>
			<span>Supporting Materials</span><a href="/static-files/q3deck">Acme Third Quarter 2026 Business Update</a> <span>1.8 MB</span></div>
			<div><span>January 12, 2026</span><a href="/events/event-details/jpm">J.P. Morgan Healthcare Conference</a>
			<a href="/static-files/jpm">Presentation</a></div>
			<a href="/static-files/notafile">Download</a>
			<a href="/static-files/guidelines">Corporate Governance Guidelines</a></body></html>`,
		"/science/publications/": `<html><head><title>Publications - Acme Bio</title></head><body>
			<div><p>March 5, 2026</p><p>NEJM</p><h4>Drugamab in Children and Adolescents with Rare Syndrome</h4><a href="/wp-content/uploads/Drugamab-NEJM-March-5-2026.pdf">View Article</a></div>
			<div><p>August 26, 2020</p><p>Science Translational Medicine</p><h4>Antisense oligonucleotides increase expression</h4><a href="https://www.science.org/doi/10.1126/x">Read Full Article</a></div>
			<div><p>October 15, 2026</p><p>CNS 2026</p><h4>Drugamab demonstrates potential as a disease-modifying therapy: indirect comparison</h4><a href="/wp-content/uploads/CNS-2026-Presentation.pdf">View Presentation</a></div>
			<div><p>September 3, 2026</p><p>EEC 2026</p><h4>Cognition, behaviour and quality of life through 48 months of treatment</h4><a href="/wp-content/uploads/EEC-2026-Poster-0797.pdf">View Poster</a></div>
			<div><h4>Untitled deck</h4><a href="/wp-content/uploads/AAN-2026-EEG.pptx">Download</a></div></body></html>`,
		"/careers/":                 `<a href="/wp-content/uploads/Job-Description.pdf">Senior Scientist role</a>`,
		"/investors/governance":     `<a href="/static-files/charter">Audit Committee Charter</a>`,
		"/investors/press-releases": `<a href="/static-files/pr1">Press release PDF</a>`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static-files/") || strings.HasPrefix(r.URL.Path, "/wp-content/") {
			if r.URL.Path == "/static-files/notafile" {
				w.Header().Set("Content-Type", "text/html")
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.4 mock"))
			return
		}
		body, found := pages[r.URL.Path]
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

func TestSiteScan(t *testing.T) {
	srv := mockSite()
	defer srv.Close()
	sc := NewSites("").Scan(context.Background(), srv.URL+"/", nil)
	got := map[string]SiteDoc{}
	for _, d := range sc.Docs {
		got[strings.TrimPrefix(d.URL, srv.URL)] = d
	}
	want := []struct{ path, kind, date, fileType, title string }{
		{"/static-files/q3deck", "Presentation", "2026-10-29", "PDF", "Acme Third Quarter 2026 Business Update"},
		{"/static-files/jpm", "Presentation", "2026-01-12", "PDF", "J.P. Morgan Healthcare Conference"},
		{"/wp-content/uploads/Drugamab-NEJM-March-5-2026.pdf", "Publication", "2026-03-05", "PDF", "Drugamab in Children and Adolescents with Rare Syndrome"},
		{"/wp-content/uploads/CNS-2026-Presentation.pdf", "Presentation", "2026-10-15", "PDF", "Drugamab demonstrates potential as a disease-modifying therapy: indirect comparison"},
		{"/wp-content/uploads/EEC-2026-Poster-0797.pdf", "Poster", "2026-09-03", "PDF", "Cognition, behaviour and quality of life through 48 months of treatment"},
		{"/wp-content/uploads/AAN-2026-EEG.pptx", "Publication", "", "PPTX", "AAN 2026 EEG"},
	}
	for _, w := range want {
		d, found := got[w.path]
		if !found {
			t.Errorf("missing %s", w.path)
			continue
		}
		if d.Kind != w.kind || d.Date != w.date || d.FileType != w.fileType || d.Title != w.title {
			t.Errorf("%s: got kind=%q date=%q type=%q title=%q", w.path, d.Kind, d.Date, d.FileType, d.Title)
		}
	}
	if len(sc.Docs) != len(want) {
		for p, d := range got {
			t.Logf("found %s (%s) %q", p, d.Kind, d.Title)
		}
		t.Errorf("found %d documents, want %d", len(sc.Docs), len(want))
	}
	for _, p := range []string{"/hidden.pdf", "/static-files/notafile", "/static-files/guidelines", "/static-files/charter", "/wp-content/uploads/Job-Description.pdf", "/static-files/pr1"} {
		if _, found := got[p]; found {
			t.Errorf("%s should not be listed", p)
		}
	}
	if len(sc.Docs) > 0 && sc.Docs[0].Date != "2026-10-29" {
		t.Errorf("newest should come first, got %s", sc.Docs[0].Date)
	}
	for _, p := range sc.Pages {
		if strings.Contains(p, "careers") || strings.Contains(p, "governance") || strings.Contains(p, "press-releases") {
			t.Errorf("should not have read %s", p)
		}
	}

	// A page the user adds is read even when the scan would not have found it.
	sc = NewSites("").Scan(context.Background(), srv.URL+"/careers/", []string{srv.URL + "/science/publications/"})
	if len(sc.Docs) != 4 {
		t.Errorf("with an added page: %d documents, want 4", len(sc.Docs))
	}
}

func TestSiteHelpers(t *testing.T) {
	if got := siteFromText("visit https://www.acmebio.com/ or www.acmebio.com. Source: www.businesswire.com; see https://clinicaltrials.gov"); got != "https://www.acmebio.com" {
		t.Errorf("siteFromText = %q", got)
	}
	for in, want := range map[string]string{"acme.com": "https://acme.com", " www.acme.com/investors ": "https://www.acme.com/investors", "not a site": "", "ftp://acme.com": ""} {
		if got := normalizeSite(in); got != want {
			t.Errorf("normalizeSite(%q) = %q, want %q", in, got, want)
		}
	}
	if baseDomain("investor.acme.com") != "acme.com" || baseDomain("ir.acme.co.uk") != "acme.co.uk" {
		t.Error("baseDomain")
	}
	for in, want := range map[string]string{"Presented Sept. 3, 2026 at EEC": "2026-09-03", "5 March 2026": "2026-03-05", "AES December 2025": "2025-12", "no date": ""} {
		if got := findDate(in); got != want {
			t.Errorf("findDate(%q) = %q, want %q", in, got, want)
		}
	}
}
