package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var mockRoot string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "secmock")
	out, err := exec.Command("python3", "testdata/make_mock.py", dir).CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	mockRoot = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func newMockSEC(t *testing.T) *SEC {
	www := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(mockRoot, "www"))))
	data := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(mockRoot, "data"))))
	t.Cleanup(www.Close)
	t.Cleanup(data.Close)
	s := NewSEC(func() string { return "tester@example.com" }, t.TempDir())
	s.WWW, s.Data = www.URL, data.URL
	return s
}

func TestSearch(t *testing.T) {
	s := newMockSEC(t)
	ctx := context.Background()
	r, err := s.Search(ctx, "stok")
	if err != nil {
		t.Fatal(err)
	}
	if len(r) < 2 || r[0].Ticker != "STOK" || r[0].CIK != 1623526 {
		t.Fatalf("unexpected %+v", r)
	}
	r, _ = s.Search(ctx, "sarepta")
	if len(r) != 1 || r[0].Ticker != "SRPT" {
		t.Fatalf("name search %+v", r)
	}
	r, _ = s.Search(ctx, "1623526")
	if len(r) != 1 || r[0].Ticker != "STOK" {
		t.Fatalf("cik search %+v", r)
	}
}

func TestNoEmail(t *testing.T) {
	s := newMockSEC(t)
	s.email = func() string { return "" }
	if _, err := s.Search(context.Background(), "STOK"); err != ErrNoEmail {
		t.Fatalf("want ErrNoEmail, got %v", err)
	}
}

func TestFilings(t *testing.T) {
	s := newMockSEC(t)
	comp, f, err := s.Filings(context.Background(), 1623526)
	if err != nil {
		t.Fatal(err)
	}
	if comp.Name != "Stoke Therapeutics, Inc." || comp.SICDesc == "" {
		t.Fatalf("company %+v", comp)
	}
	if len(f) != 25 {
		t.Fatalf("want 25 filings (24 recent + 1 archived), got %d", len(f))
	}
	if f[0].Filed < f[len(f)-1].Filed || f[len(f)-1].Form != "S-1" {
		t.Fatalf("ordering wrong: first %s last %s %s", f[0].Filed, f[len(f)-1].Filed, f[len(f)-1].Form)
	}
	if !strings.HasSuffix(f[0].IndexURL, "-index.htm") || !strings.Contains(f[0].URL, "/Archives/edgar/data/1623526/") {
		t.Fatalf("urls %s %s", f[0].URL, f[0].IndexURL)
	}
}

func TestFilingDocs(t *testing.T) {
	s := newMockSEC(t)
	ctx := context.Background()
	_, f, _ := s.Filings(ctx, 1623526)
	var eightK, tenQ, s1, form4 Filing
	for _, x := range f {
		switch {
		case x.Form == "8-K" && eightK.Acc == "":
			eightK = x
		case x.Form == "10-Q" && tenQ.Acc == "":
			tenQ = x
		case x.Form == "S-1":
			s1 = x
		case x.Form == "4" && form4.Acc == "":
			form4 = x
		}
	}
	docs, err := s.FilingDocs(ctx, 1623526, eightK.Acc)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, d := range docs {
		kinds[d.Name] = d.Kind
		if strings.Contains(d.URL, "/ix?doc=") || !strings.HasPrefix(d.URL, s.WWW+"/Archives/") {
			t.Fatalf("bad url %s", d.URL)
		}
	}
	if kinds[eightK.PrimaryDoc] != "main" || kinds["stok-ex99_1.htm"] != "exhibit" || kinds["g1.gif"] != "graphic" || kinds[eightK.Acc+".txt"] != "full" || kinds["stok-20250930.xsd"] != "data" {
		t.Fatalf("kinds %v", kinds)
	}
	ex := 0
	for _, d := range docs {
		if wantExhibit(d, "ex99") {
			ex++
		}
	}
	if ex != 1 {
		t.Fatalf("ex99 count %d", ex)
	}
	docs, _ = s.FilingDocs(ctx, 1623526, tenQ.Acc)
	all, ex99 := 0, 0
	for _, d := range docs {
		if wantExhibit(d, "all") {
			all++
		}
		if wantExhibit(d, "ex99") {
			ex99++
		}
	}
	if all != 2 || ex99 != 0 {
		t.Fatalf("10-Q exhibits all=%d ex99=%d", all, ex99)
	}
	docs, _ = s.FilingDocs(ctx, 1623526, form4.Acc)
	mains := 0
	for _, d := range docs {
		if d.Kind == "main" {
			mains++
		}
	}
	if mains != 1 {
		t.Fatalf("form 4 main docs %d: %+v", mains, docs)
	}
	// S-1 has no -index.htm, falls back to index.json
	docs, err = s.FilingDocs(ctx, 1623526, s1.Acc)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, d := range docs {
		types = append(types, d.Name+":"+d.Kind+":"+d.Type)
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "d123dex991.htm:exhibit:EX-99.1") || strings.Contains(joined, "R1.htm") || !strings.Contains(joined, ".txt:full") {
		t.Fatalf("fallback docs %s", joined)
	}
	if _, err := s.FilingDocs(ctx, 1623526, "../../etc"); err == nil {
		t.Fatal("expected invalid accession error")
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1 }

func TestFinancials(t *testing.T) {
	s := newMockSEC(t)
	fin, err := s.Financials(context.Background(), 1623526)
	if err != nil {
		t.Fatal(err)
	}
	q := fin.Quarterly
	if len(q.Columns) != 12 || q.Columns[11].Key != "2025-09-30" || q.Columns[0].Key != "2022-12-31" {
		var ks []string
		for _, c := range q.Columns {
			ks = append(ks, c.Key)
		}
		t.Fatalf("columns %v", ks)
	}
	col := func(k string) int {
		for i, c := range q.Columns {
			if c.Key == k {
				return i
			}
		}
		t.Fatalf("no column %s", k)
		return -1
	}
	rd := q.row("rd")
	// Q4 2024 R&D derived = FY - 9M YTD; synthetic Q4 2024 = 20e6+6e6+1.5e6
	c := rd.Cells[col("2024-12-31")]
	if c == nil || !c.D || !approx(c.V, 27.5e6) {
		t.Fatalf("derived Q4 R&D %+v", c)
	}
	// restated comparative wins
	if c := rd.Cells[col("2024-09-30")]; c == nil || c.D || c.V != 27777777 {
		t.Fatalf("restated Q3 2024 %+v", c)
	}
	// OCF is YTD-only in the mock: Q2 2025 must be derived 6M-3M
	ocf := q.row("ocf")
	// Q2 2025 synthetic: R&D 29.5M, G&A 12M, revenue 5M, OCF = -(29.5+12-5)*0.85
	exp := -(29.5e6 + 12e6 - 5e6) * 0.85
	if c := ocf.Cells[col("2025-06-30")]; c == nil || !c.D || !approx(c.V, exp) {
		t.Fatalf("derived OCF Q2 2025 %+v want %v", c, exp)
	}
	if c := ocf.Cells[col("2025-03-31")]; c == nil || c.D {
		t.Fatalf("Q1 OCF should be direct %+v", c)
	}
	ci := q.row("cashinv")
	if c := ci.Cells[col("2025-09-30")]; c == nil || !approx(c.V, (80e6-15e6-4e6)+(150e6-60e6-8e6)) {
		t.Fatalf("cash+inv %+v", c)
	}
	if q.row("shares").Cells[col("2025-09-30")] == nil {
		t.Fatal("cover shares not mapped")
	}
	if eps := q.row("eps"); eps.Cells[col("2024-12-31")] != nil {
		t.Fatal("EPS must not be derived for Q4")
	}
	r := fin.Runway
	if r == nil || r.AsOf != "2025-09-30" || r.Quarters <= 0 || r.Through == "" || r.BurnBasis != 4 {
		t.Fatalf("runway %+v", r)
	}
	if len(fin.Annual.Columns) != 3 || fin.Annual.Columns[2].Key != "2024-12-31" {
		t.Fatalf("annual cols %+v", fin.Annual.Columns)
	}
	csv := q.CSV()
	if !strings.HasPrefix(csv, "Metric,Unit,2022-12-31") || !strings.Contains(csv, "Research & development,USD,") {
		t.Fatalf("csv %s", csv[:200])
	}
}

func TestNewSince(t *testing.T) {
	f := []Filing{{Acc: "c", Filed: "2025-11-06"}, {Acc: "b", Filed: "2025-11-01"}, {Acc: "a", Filed: "2025-10-01"}}
	if n := newSince(WatchItem{LastSeenAcc: "b", LastSeenFiled: "2025-11-01"}, f); len(n) != 1 || n[0] != "c" {
		t.Fatalf("%v", n)
	}
	if n := newSince(WatchItem{LastSeenAcc: "zz", LastSeenFiled: "2025-10-15"}, f); len(n) != 2 {
		t.Fatalf("%v", n)
	}
	if n := newSince(WatchItem{}, f); len(n) != 0 {
		t.Fatalf("%v", n)
	}
}

func TestNames(t *testing.T) {
	it := DownloadItem{Ticker: "stok", Form: "10-K/A", Acc: "0001623526-25-000099", Filed: "2025-03-04"}
	if got := filingBaseName(it); got != "STOK 10-K-A 2025-03-04" {
		t.Fatal(got)
	}
	it = DownloadItem{Ticker: "STOK", Form: "10-Q", Filed: "2026-11-03"}
	if got := filingBaseName(it); got != "STOK 10-Q 2026-11-03" {
		t.Fatal(got)
	}
	if sanitizeName(`a:b*c?"d"|e.`) != "a-bcd-e" {
		t.Fatal(sanitizeName(`a:b*c?"d"|e.`))
	}
	if !within(`/x/SEC Filings`, `/x/SEC Filings/STOK/a.pdf`) || within(`/x/SEC Filings`, `/x/other`) || within(`/x/SEC Filings`, `/x/SEC Filings/../y`) {
		t.Fatal("within")
	}
}

func TestResolveOutput(t *testing.T) {
	dir := t.TempDir()
	write := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("pdf"), 0o644)
	}
	// first 8-K of the day gets the plain name
	p1, have := resolveOutput(dir, "8-K", "STOK 8-K 2026-11-03", "accA|a.htm")
	if have || filepath.Base(p1) != "STOK 8-K 2026-11-03.pdf" {
		t.Fatal(p1, have)
	}
	write(p1)
	recordOutput(dir, "accA|a.htm", p1)
	// same document again -> skipped
	if p, have := resolveOutput(dir, "8-K", "STOK 8-K 2026-11-03", "accA|a.htm"); !have || p != p1 {
		t.Fatal("expected skip", p, have)
	}
	// a different 8-K filed the same day -> (2), not an overwrite or a false skip
	p2, have := resolveOutput(dir, "8-K", "STOK 8-K 2026-11-03", "accB|b.htm")
	if have || filepath.Base(p2) != "STOK 8-K 2026-11-03 (2).pdf" {
		t.Fatal(p2, have)
	}
	write(p2)
	recordOutput(dir, "accB|b.htm", p2)
	if p, have := resolveOutput(dir, "8-K", "STOK 8-K 2026-11-03", "accB|b.htm"); !have || p != p2 {
		t.Fatal("expected skip of (2)", p, have)
	}
	// user deleted the file -> download again to the same name
	os.Remove(p1)
	if p, have := resolveOutput(dir, "8-K", "STOK 8-K 2026-11-03", "accA|a.htm"); have || p != p1 {
		t.Fatal("expected re-download", p, have)
	}
}

// Reproduces the Windows behaviour: the browser process exits immediately with success
// and the PDF appears a few seconds later.
func TestPrinterLateFile(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-edge.sh")
	os.WriteFile(fake, []byte(`#!/bin/sh
for a in "$@"; do case "$a" in --print-to-pdf=*) OUT="${a#--print-to-pdf=}";; esac; done
( sleep 3; head -c 2000 /dev/zero > "$OUT.w"; sleep 1; head -c 4000 /dev/zero >> "$OUT.w"; mv "$OUT.w" "$OUT" ) >/dev/null 2>&1 &
exit 0
`), 0o755)
	p := &Printer{Browser: fake, profiles: filepath.Join(dir, "profiles"), ua: func() string { return "x" }}
	out := filepath.Join(dir, "STOK", "10-Q", "STOK 10-Q 2026-11-03.pdf")
	if err := p.PrintToPDF(context.Background(), "http://example/doc.htm", out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Size() != 6000 {
		t.Fatalf("output %v %v", st, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "STOK", "10-Q", "~printing-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestInjectBase(t *testing.T) {
	out := string(injectBase([]byte(`<HTML><HEAD><title>x</title></HEAD><body></body></HTML>`), "https://www.sec.gov/Archives/edgar/data/1/2/"))
	if !strings.HasPrefix(out, `<HTML><HEAD><base href="https://www.sec.gov/Archives/edgar/data/1/2/" target="_blank">`) {
		t.Fatal(out)
	}
	out = string(injectBase([]byte(`<body>x</body>`), "https://e/"))
	if !strings.HasPrefix(out, `<base href="https://e/"`) {
		t.Fatal(out)
	}
}

func TestKeepJumpsInDoc(t *testing.T) {
	self := "http://127.0.0.1:5000/api/view?url=https%3A%2F%2Fwww.sec.gov%2FArchives%2Fa%2Fstok-10k.htm&t=abc"
	in := `<a href="#item1">Business</a> <A style="x" HREF='stok-10k.htm#item1a'>Risk</A> <a href="ex21.htm#top">Ex</a> <a href="https://www.sec.gov/">SEC</a> <a name="item1"></a> <a href=#item7>MD&amp;A</a>`
	out := string(keepJumpsInDoc([]byte(in), self, "stok-10k.htm"))
	esc := htmlEscape(self)
	for _, want := range []string{
		`<a target="_self" href="` + esc + `#item1">Business`,
		`<a target="_self" style="x" HREF="` + esc + `#item1a">Risk`,
		`<a href="ex21.htm#top">Ex`, `<a target="_self" href="` + esc + `#item7">MD`, `<a href="https://www.sec.gov/">SEC`, `<a name="item1"></a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s\nin %s", want, out)
		}
	}
}

func TestSessions(t *testing.T) {
	dir := t.TempDir()
	st := OpenStore(filepath.Join(dir, "cfg"), "")
	_ = st.Update(func(s *Settings) {
		s.Email = "a@b.co"
		s.Watchlist = []WatchItem{{Ticker: "STOK", CIK: 1}}
	})
	local := DefaultModel(nil, nil, 2026)
	local.Beta = 1.1
	if err := st.SaveModel(1, &local); err != nil {
		t.Fatal(err)
	}

	// Saving as a file copies the local session into it and switches to it.
	mine := filepath.Join(dir, "usb", "mine.secq")
	if err := st.SaveSessionAs(mine, false); err != nil {
		t.Fatal(err)
	}
	if m, saved := st.LoadModel(1, ""); !saved || m.Beta != 1.1 || len(st.Get().Watchlist) != 1 || st.SessionFile() != mine {
		t.Fatalf("session did not carry the local work over")
	}
	// Changes now go to the file, not to this computer's session.
	_ = st.Update(func(s *Settings) { s.Watchlist = append(s.Watchlist, WatchItem{Ticker: "IONS", CIK: 2}) })
	m2 := DefaultModel(nil, nil, 2026)
	m2.Beta = 2
	_ = st.SaveModel(1, &m2)
	b, _ := os.ReadFile(mine)
	if strings.Contains(string(b), "a@b.co") {
		t.Error("the session file must not hold the contact email")
	}
	if s, err := readSession(mine); err != nil || len(s.Watchlist) != 2 || s.Sets["1"].Versions[0].Beta != 2 {
		t.Fatalf("file not updated: %v", err)
	}

	// A second person's empty session, then back and forth.
	theirs := filepath.Join(dir, "theirs.secq")
	if err := st.SaveSessionAs(theirs, true); err != nil {
		t.Fatal(err)
	}
	if _, saved := st.LoadModel(1, ""); saved || len(st.Get().Watchlist) != 0 {
		t.Error("a new session should start empty")
	}
	if err := st.CloseSession(); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.LoadModel(1, ""); m == nil || m.Beta != 1.1 || len(st.Get().Watchlist) != 1 {
		t.Error("this computer's session should be untouched by work done in a file")
	}
	if err := st.OpenSession(mine); err != nil {
		t.Fatal(err)
	}
	if got := st.Get(); len(got.Watchlist) != 2 || got.Email != "a@b.co" || len(got.RecentSessions) != 2 || got.RecentSessions[0] != mine {
		t.Errorf("after reopening: %+v", got)
	}

	// The app reopens the file next time, and copes when it has gone.
	if again := OpenStore(filepath.Join(dir, "cfg"), ""); again.SessionFile() != mine || len(again.Get().Watchlist) != 2 {
		t.Error("session not reopened at startup")
	}
	_ = os.Remove(mine)
	gone := OpenStore(filepath.Join(dir, "cfg"), "")
	if gone.SessionFile() != "" || gone.Missing != mine || len(gone.Get().Watchlist) != 1 {
		t.Errorf("missing file: file=%q missing=%q", gone.SessionFile(), gone.Missing)
	}
	if err := gone.OpenSession(filepath.Join(dir, "cfg", "settings.json")); err == nil {
		t.Error("a file that is not a session should be refused")
	}
}

func TestModelVersions(t *testing.T) {
	dir := t.TempDir()
	st := OpenStore(filepath.Join(dir, "cfg"), "")
	// A model file written before versions existed becomes the "Base case".
	old := DefaultModel(nil, nil, 2026)
	old.Beta = 1.3
	b, _ := json.Marshal(old)
	_ = os.MkdirAll(filepath.Join(dir, "cfg", "models"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cfg", "models", "7.json"), b, 0o644)
	if m, saved := st.LoadModel(7, ""); !saved || m.Name != defaultModelName || m.Beta != 1.3 {
		t.Fatalf("legacy model not read as Base case")
	}
	bull := old
	bull.Name, bull.Beta = "Bull case", 0.9
	if err := st.SaveModel(7, &bull); err != nil {
		t.Fatal(err)
	}
	set := st.Models(7)
	if len(set.Versions) != 2 || set.Active != "Bull case" {
		t.Fatalf("after save as: %v active %q", set.names(), set.Active)
	}
	if m, _ := st.LoadModel(7, "base CASE"); m == nil || m.Beta != 1.3 {
		t.Error("named version not found")
	}
	bull.Beta = 0.8 // saving under the same name replaces it
	_ = st.SaveModel(7, &bull)
	if m, _ := st.LoadModel(7, ""); len(st.Models(7).Versions) != 2 || m.Beta != 0.8 {
		t.Error("save did not replace the same-named version")
	}
	st.UseModel(7, "Base case")
	if st.Models(7).Active != "Base case" {
		t.Error("UseModel did not switch")
	}
	_ = st.DeleteModel(7, "Base case")
	if set := st.Models(7); len(set.Versions) != 1 || set.Active != "Bull case" {
		t.Errorf("after delete: %v active %q", set.names(), set.Active)
	}
	_ = st.DeleteModel(7, "Bull case")
	if _, err := os.Stat(filepath.Join(dir, "cfg", "models", "7.json")); err == nil {
		t.Error("empty set should remove the file")
	}

	// A format 1 session file (one model per company) opens with that model as Base case.
	f1 := filepath.Join(dir, "old.secq")
	_ = os.WriteFile(f1, []byte(`{"app":"SECquence session","format":1,"watchlist":[],"models":{"7":`+string(b)+`}}`), 0o644)
	if err := st.OpenSession(f1); err != nil {
		t.Fatal(err)
	}
	if m, saved := st.LoadModel(7, ""); !saved || m.Name != defaultModelName {
		t.Error("format 1 session model not carried over")
	}
	_ = st.SetSite(7, SiteConfig{URL: "https://example.com", Pages: []string{"https://example.com/a"}})
	if s, _ := readSession(f1); s.Format != 2 || s.Sites["7"].URL != "https://example.com" || len(s.Models) != 0 {
		t.Errorf("session not rewritten in the new format: %+v", s)
	}
}
