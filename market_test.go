package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func startMockMarket(t *testing.T, args ...string) string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("python3", append([]string{"testdata/mock_market.py", fmt.Sprint(port)}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		if resp, err := http.Get(base + "/nothing"); err == nil {
			resp.Body.Close()
			return base
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("mock market did not start")
	return ""
}

func mockMarket(base string) *Market {
	m := NewMarket()
	m.Yahoo, m.Stooq, m.GNews, m.YNews, m.CTGov = []string{base}, base, base, base, base
	return m
}

func TestChart(t *testing.T) {
	m := mockMarket(startMockMarket(t))
	ctx := context.Background()
	for _, r := range []string{"1d", "5d", "1mo", "6mo", "ytd", "1y", "5y", "max"} {
		c, err := m.Chart(ctx, "stok", r)
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if len(c.Points) < 10 || c.Price != 12.34 || c.Source != "Yahoo Finance" || c.TimeZone != "America/New_York" || c.High52 == 0 {
			t.Fatalf("%s: %+v", r, c)
		}
		for i := 1; i < len(c.Points); i++ {
			if c.Points[i].T <= c.Points[i-1].T || c.Points[i].C == 0 {
				t.Fatalf("%s: bad points at %d", r, i)
			}
		}
		if r == "1d" && (c.PrevClose != 12.02 || len(c.Points) != 77) { // one null close skipped
			t.Fatalf("1d prev %v points %d", c.PrevClose, len(c.Points))
		}
	}
	if _, err := m.Chart(ctx, "NOPE", "1d"); err == nil || !strings.Contains(err.Error(), "no price data") {
		t.Fatalf("unknown ticker: %v", err)
	}
	if _, err := m.Chart(ctx, "STOK; rm", "1d"); err == nil {
		t.Fatal("expected invalid ticker")
	}
}

func TestChartFallback(t *testing.T) {
	m := mockMarket(startMockMarket(t, "--fail-yahoo"))
	ctx := context.Background()
	c, err := m.Chart(ctx, "STOK", "1y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Source, "Stooq") || len(c.Points) < 40 || c.Price != 12.34 || c.Note == "" {
		t.Fatalf("%+v", c)
	}
	if _, err := m.Chart(ctx, "STOK", "1d"); err == nil {
		t.Fatal("intraday has no fallback; expected error")
	}
}

func TestNews(t *testing.T) {
	m := mockMarket(startMockMarket(t))
	items, problems, err := m.News(context.Background(), "STOK", "Stoke Therapeutics, Inc.", false)
	if err != nil || len(problems) != 0 {
		t.Fatal(err, problems)
	}
	if len(items) != 7 { // 5 + 3, one duplicate headline merged
		for _, i := range items {
			t.Log(i.Feed, i.Title)
		}
		t.Fatalf("got %d items", len(items))
	}
	for i := 1; i < len(items); i++ {
		if items[i].Published > items[i-1].Published {
			t.Fatal("not sorted newest first")
		}
	}
	for _, it := range items {
		if strings.HasSuffix(it.Title, " - GlobeNewswire") || it.Source == "" || it.Published == "" {
			t.Fatalf("bad item %+v", it)
		}
		if !m.AllowedLink(it.URL) {
			t.Fatal("link not allowed")
		}
		if it.Feed == "Google News" && it.Summary != "" {
			t.Fatalf("google summary should be dropped: %q", it.Summary)
		}
	}
	if m.AllowedLink("https://evil.example.com/") {
		t.Fatal("unexpected link allowed")
	}
}

func TestCleanCompanyName(t *testing.T) {
	for in, want := range map[string]string{
		"Stoke Therapeutics, Inc.":        "Stoke Therapeutics",
		"Sarepta Therapeutics, Inc.":      "Sarepta Therapeutics",
		"Ionis Pharmaceuticals Inc":       "Ionis Pharmaceuticals",
		"VERTEX PHARMACEUTICALS INC / MA": "VERTEX PHARMACEUTICALS",
		"AstraZeneca PLC":                 "AstraZeneca",
		"Apple Inc.":                      "Apple",
	} {
		if got := cleanCompanyName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
