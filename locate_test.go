package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvenance(t *testing.T) {
	s := newMockSEC(t)
	fin, err := s.Financials(context.Background(), 1623526)
	if err != nil {
		t.Fatal(err)
	}
	q := fin.Quarterly
	idx := func(k string) int {
		for i, c := range q.Columns {
			if c.Key == k {
				return i
			}
		}
		t.Fatalf("no col %s", k)
		return -1
	}
	// direct value: one source, from the 10-Q filed 2025-11-06
	c := q.row("rd").Cells[idx("2025-09-30")]
	if c.Op != "" || len(c.Src) != 1 || c.Src[0].Form != "10-Q" || c.Src[0].Filed != "2025-11-06" || c.Src[0].Concept != "us-gaap:ResearchAndDevelopmentExpense" || c.Src[0].Accn == "" || c.Src[0].Role != "3 months" {
		t.Fatalf("direct src %+v", c)
	}
	// restated value lists the original filing as an alternative with a different value
	c = q.row("rd").Cells[idx("2024-09-30")]
	if len(c.Alt) == 0 || c.Alt[0].Val == c.V || c.Alt[0].Filed != "2024-11-06" {
		t.Fatalf("alt %+v", c.Alt)
	}
	// derived Q4: FY (10-K) minus 9M YTD (10-Q)
	c = q.row("rd").Cells[idx("2024-12-31")]
	if c.Op != "sub" || len(c.Src) != 2 || c.Src[0].Role != "Full year" || c.Src[0].Form != "10-K" || c.Src[1].Role != "9 months year-to-date" || c.Src[1].Form != "10-Q" {
		t.Fatalf("derived %+v", c)
	}
	if c.V != c.Src[0].Val-c.Src[1].Val {
		t.Fatal("derived math")
	}
	// sum: components labelled
	c = q.row("cashinv").Cells[idx("2025-09-30")]
	if c.Op != "sum" || len(c.Src) != 2 || c.Src[0].Role != "Cash & cash equivalents" || c.Src[1].Role != "Short-term investments" || c.V != c.Src[0].Val+c.Src[1].Val {
		t.Fatalf("sum %+v", c)
	}
}

func TestLocate(t *testing.T) {
	s := newMockSEC(t)
	ctx := context.Background()
	fin, _ := s.Financials(ctx, 1623526)
	q := fin.Quarterly
	check := func(name string, src Src, wantShown string) *LocateResult {
		r, err := s.Locate(ctx, 1623526, src.Accn, src.Concept, src.Start, src.End, src.Val)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(r.Hits) == 0 || !r.ValueMatch || !r.Inline || r.Shown != wantShown {
			t.Fatalf("%s: %+v", name, r)
		}
		return r
	}
	last := len(q.Columns) - 1
	rd := q.row("rd").Cells[last].Src[0]
	r := check("R&D Q3 2025", rd, "30,000")
	if r.Scale != 3 || !strings.HasSuffix(r.URL, "stok-20250930.htm") {
		t.Fatalf("%+v", r)
	}
	// the dimensional (segment) R&D fact must not be among the hits
	body, _, _ := s.FetchCached(ctx, r.URL, 0)
	_, facts := parseIX(string(body))
	for _, h := range r.Hits {
		if facts[h].Ctx == "c-dim" {
			t.Fatal("dimensional fact matched")
		}
	}
	// negative values are tagged sign="-"
	ni := q.row("netinc").Cells[last].Src[0]
	if r := check("net loss", ni, "34,000"); r.Sign != "-" || r.DocValue >= 0 {
		t.Fatalf("sign %+v", r)
	}
	// instant (balance sheet)
	check("cash", q.row("cash").Cells[last].Src[0], "61,000")
	// both inputs of a derived Q4 value
	for i := 0; i < len(q.Columns); i++ {
		if c := q.row("rd").Cells[i]; c != nil && c.Op == "sub" {
			if c.Src[0].Form == "10-K" && c.Src[0].Filed == "2025-03-04" {
				check("FY input", c.Src[0], "107,000")
				check("9M input", c.Src[1], "79,500")
			}
		}
	}
	// hidden (ix:header) fact is found but flagged
	sh := q.row("shares").Cells[last].Src[0]
	r, err := s.Locate(ctx, 1623526, sh.Accn, sh.Concept, "", sh.End, sh.Val)
	if err != nil || len(r.Hits) == 0 || r.Message == "" {
		t.Fatalf("hidden %+v %v", r, err)
	}
	// a filing with no inline XBRL
	r, err = s.Locate(ctx, 1623526, "0001193125-19-140001", "us-gaap:Assets", "", "2019-03-31", 1)
	if err != nil || len(r.Hits) != 0 || r.Inline || r.Message == "" || r.URL == "" {
		t.Fatalf("non-inline %+v %v", r, err)
	}
}

func TestHighlight(t *testing.T) {
	doc := []byte(`<html><head><title>x</title></head><body><ix:nonFraction name="a">1</ix:nonFraction> <ix:nonFraction name="b">2</ix:nonFraction> <IX:NONFRACTION name="c">3</IX:NONFRACTION></body></html>`)
	out := string(highlightIX(doc, []int{2, 1}))
	if !strings.Contains(out, `<IX:NONFRACTION id="secq-hit" data-secq-hit="1" name="c">`) || !strings.Contains(out, `<ix:nonFraction data-secq-hit="1" name="b">`) || !strings.Contains(out, `<ix:nonFraction name="a">`) || !strings.Contains(out, "<head><style>") {
		t.Fatal(out)
	}
}

func TestXLSX(t *testing.T) {
	s := newMockSEC(t)
	fin, _ := s.Financials(context.Background(), 1623526)
	b, err := financialsXLSX(fin, fin.Quarterly, "quarterly", "STOK", "https://www.sec.gov")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(zr.File) != 7 {
		t.Fatalf("zip %v %d", err, len(zr.File))
	}
	p := filepath.Join(t.TempDir(), "x.xlsx")
	os.WriteFile(p, b, 0o644)
	// validate with openpyxl when available
	script := `
import sys, openpyxl
wb = openpyxl.load_workbook(sys.argv[1])
print(wb.sheetnames)
ws = wb["Sources"]
rows = list(ws.iter_rows(values_only=True))
print(len(rows)); print(rows[0]); print(rows[1])
assert wb["Financials"]["A1"].value.startswith("Stoke")
`
	out, err := exec.Command("python3", "-c", script, p).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No module named") {
			t.Skip("openpyxl not installed")
		}
		t.Fatalf("%v %s", err, out)
	}
	t.Logf("%s", out)
	if !strings.Contains(string(out), "['Financials', 'Sources']") || !strings.Contains(string(out), "HYPERLINK") {
		t.Fatalf("%s", out)
	}
}
