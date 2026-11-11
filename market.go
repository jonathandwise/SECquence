package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stock prices and news. Neither has an official free API, so the app reads the
// public endpoints a browser would:
//   prices  Yahoo Finance chart API; Stooq daily closes as a fallback
//   news    Google News RSS (by company name) and Yahoo Finance RSS (by ticker)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"

type Market struct {
	hc    *http.Client
	Yahoo []string // chart API hosts, tried in order
	Stooq string
	GNews string
	YNews string
	CTGov string

	mu    sync.Mutex
	cache map[string]cached

	lmu   sync.Mutex
	links map[string]bool // outside links the app has shown and may open
}

func NewMarket() *Market {
	return &Market{
		hc:    &http.Client{Timeout: 30 * time.Second},
		Yahoo: strings.Split(envOr("SECQ_YAHOO", "https://query1.finance.yahoo.com,https://query2.finance.yahoo.com"), ","),
		Stooq: envOr("SECQ_STOOQ", "https://stooq.com"),
		GNews: envOr("SECQ_GNEWS", "https://news.google.com"),
		YNews: envOr("SECQ_YNEWS", "https://feeds.finance.yahoo.com"),
		CTGov: envOr("SECQ_CTGOV", "https://clinicaltrials.gov"),
		cache: map[string]cached{},
		links: map[string]bool{},
	}
}

// get fetches a URL with a browser-like identity, caching successes for ttl. On an
// HTTP error it still returns the body, which may explain the failure.
func (m *Market) get(ctx context.Context, u string, ttl time.Duration) ([]byte, error) {
	m.mu.Lock()
	c, ok := m.cache[u]
	m.mu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.body, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	m.mu.Lock()
	if len(m.cache) > 300 {
		m.cache = map[string]cached{}
	}
	m.cache[u] = cached{body: body, at: time.Now()}
	m.mu.Unlock()
	return body, nil
}

func (m *Market) allowLinks(urls ...string) {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	if len(m.links) > 8000 {
		m.links = map[string]bool{}
	}
	for _, u := range urls {
		m.links[u] = true
	}
}

// AllowedLink reports whether the app itself showed this link (so it may be opened).
func (m *Market) AllowedLink(u string) bool {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	return m.links[u]
}

// ---------------------------------------------------------------- prices

type PricePoint struct {
	T int64   `json:"t"` // unix seconds
	C float64 `json:"c"` // close
	V float64 `json:"v"` // volume
}

type Chart struct {
	Ticker     string       `json:"ticker"`
	Name       string       `json:"name"`
	Range      string       `json:"range"`
	Interval   string       `json:"interval"`
	Currency   string       `json:"currency"`
	Exchange   string       `json:"exchange"`
	TimeZone   string       `json:"timeZone"`
	Price      float64      `json:"price"`
	PrevClose  float64      `json:"prevClose"`
	DayHigh    float64      `json:"dayHigh"`
	DayLow     float64      `json:"dayLow"`
	Volume     float64      `json:"volume"`
	High52     float64      `json:"high52"`
	Low52      float64      `json:"low52"`
	MarketTime int64        `json:"marketTime"`
	Points     []PricePoint `json:"points"`
	Source     string       `json:"source"`
	Note       string       `json:"note,omitempty"`
}

type rangeSpec struct {
	yahoo, interval string
	ttl             time.Duration
	days            int // window for the daily fallback: 0 none, -1 since January 1
}

var chartRanges = map[string]rangeSpec{
	"1d":  {"1d", "5m", time.Minute, 0},
	"5d":  {"5d", "15m", 3 * time.Minute, 0},
	"1mo": {"1mo", "60m", 10 * time.Minute, 31},
	"6mo": {"6mo", "1d", 30 * time.Minute, 183},
	"ytd": {"ytd", "1d", 30 * time.Minute, -1},
	"1y":  {"1y", "1d", 30 * time.Minute, 366},
	"5y":  {"5y", "1wk", time.Hour, 5*365 + 2},
	"max": {"max", "1wk", time.Hour, 1 << 20},
}

var reTicker = regexp.MustCompile(`^[A-Z0-9.\-^=]{1,15}$`)

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func parseYahooChart(body []byte) (*Chart, error) {
	var y struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Currency             string  `json:"currency"`
					Symbol               string  `json:"symbol"`
					FullExchangeName     string  `json:"fullExchangeName"`
					ExchangeName         string  `json:"exchangeName"`
					ExchangeTimezoneName string  `json:"exchangeTimezoneName"`
					RegularMarketPrice   float64 `json:"regularMarketPrice"`
					ChartPreviousClose   float64 `json:"chartPreviousClose"`
					PreviousClose        float64 `json:"previousClose"`
					RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
					RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
					RegularMarketVolume  float64 `json:"regularMarketVolume"`
					FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
					FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
					RegularMarketTime    int64   `json:"regularMarketTime"`
					LongName             string  `json:"longName"`
					ShortName            string  `json:"shortName"`
					DataGranularity      string  `json:"dataGranularity"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Close  []*float64 `json:"close"`
						Volume []*float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &y); err != nil {
		return nil, errors.New("unexpected response from Yahoo Finance")
	}
	if e := y.Chart.Error; e != nil && e.Description != "" {
		return nil, errors.New(e.Description)
	}
	if len(y.Chart.Result) == 0 {
		return nil, errors.New("no price data")
	}
	r := y.Chart.Result[0]
	mt := r.Meta
	c := &Chart{
		Ticker: mt.Symbol, Name: firstNonEmpty(mt.LongName, mt.ShortName), Currency: mt.Currency,
		Exchange: firstNonEmpty(mt.FullExchangeName, mt.ExchangeName), TimeZone: mt.ExchangeTimezoneName,
		Price: mt.RegularMarketPrice, PrevClose: mt.PreviousClose, DayHigh: mt.RegularMarketDayHigh, DayLow: mt.RegularMarketDayLow,
		Volume: mt.RegularMarketVolume, High52: mt.FiftyTwoWeekHigh, Low52: mt.FiftyTwoWeekLow, MarketTime: mt.RegularMarketTime,
		Interval: mt.DataGranularity, Source: "Yahoo Finance", Points: []PricePoint{},
	}
	if c.PrevClose == 0 {
		c.PrevClose = mt.ChartPreviousClose
	}
	if len(r.Indicators.Quote) > 0 {
		q := r.Indicators.Quote[0]
		for i, t := range r.Timestamp {
			if i >= len(q.Close) || q.Close[i] == nil { // gaps come back as null
				continue
			}
			p := PricePoint{T: t, C: *q.Close[i]}
			if i < len(q.Volume) && q.Volume[i] != nil {
				p.V = *q.Volume[i]
			}
			c.Points = append(c.Points, p)
		}
	}
	return c, nil
}

func (m *Market) Chart(ctx context.Context, ticker, rng string) (*Chart, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if !reTicker.MatchString(ticker) {
		return nil, fmt.Errorf("invalid ticker %q", ticker)
	}
	spec, ok := chartRanges[rng]
	if !ok {
		return nil, fmt.Errorf("unknown range %q", rng)
	}
	var last error
	for _, host := range m.Yahoo {
		u := fmt.Sprintf("%s/v8/finance/chart/%s?range=%s&interval=%s&includePrePost=false&events=div%%2Csplit",
			strings.TrimRight(host, "/"), url.PathEscape(ticker), spec.yahoo, spec.interval)
		body, err := m.get(ctx, u, spec.ttl)
		if err != nil {
			// Unknown symbols come back as an HTTP error with a JSON explanation.
			if _, perr := parseYahooChart(body); perr != nil && strings.Contains(strings.ToLower(perr.Error()), "no data") {
				return nil, fmt.Errorf("Yahoo Finance has no price data for %s (%v)", ticker, perr)
			}
			last = err
			continue
		}
		c, err := parseYahooChart(body)
		if err != nil {
			last = err
			continue
		}
		c.Range = rng
		if c.Ticker == "" {
			c.Ticker = ticker
		}
		return c, nil
	}
	logf("yahoo chart %s %s failed: %v", ticker, rng, last)
	if spec.days != 0 { // intraday ranges have no fallback
		c, err := m.stooqChart(ctx, ticker, rng, spec.days)
		if err == nil {
			return c, nil
		}
		logf("stooq %s failed: %v", ticker, err)
	}
	return nil, fmt.Errorf("couldn't load prices for %s right now (Yahoo Finance: %v). Try again in a minute", ticker, last)
}

func (m *Market) stooqChart(ctx context.Context, ticker, rng string, days int) (*Chart, error) {
	sym := strings.ToLower(strings.ReplaceAll(ticker, ".", "-")) + ".us"
	body, err := m.get(ctx, fmt.Sprintf("%s/q/d/l/?s=%s&i=d", strings.TrimRight(m.Stooq, "/"), url.QueryEscape(sym)), time.Hour)
	if err != nil {
		return nil, err
	}
	recs, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || len(recs) < 2 || len(recs[0]) < 5 || !strings.EqualFold(recs[0][0], "Date") {
		return nil, errors.New("no data")
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.FixedZone("ET", -5*3600)
	}
	var all []PricePoint
	for _, r := range recs[1:] {
		d, err1 := time.ParseInLocation("2006-01-02", r[0], ny)
		cl, err2 := strconv.ParseFloat(r[4], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		p := PricePoint{T: d.Add(16 * time.Hour).Unix(), C: cl}
		if len(r) > 5 {
			p.V, _ = strconv.ParseFloat(r[5], 64)
		}
		all = append(all, p)
	}
	if len(all) == 0 {
		return nil, errors.New("no data")
	}
	now := time.Now()
	cutoff := now.AddDate(0, 0, -days).Unix()
	if days < 0 {
		cutoff = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, ny).Unix()
	}
	lastP := all[len(all)-1]
	c := &Chart{Ticker: ticker, Range: rng, Interval: "1d", Currency: "USD", TimeZone: "America/New_York",
		Price: lastP.C, MarketTime: lastP.T, Volume: lastP.V, Source: "Stooq (daily closes)",
		Note: "Yahoo Finance was unavailable, so this shows daily closing prices from Stooq."}
	if len(all) > 1 {
		c.PrevClose = all[len(all)-2].C
	}
	yearAgo := now.AddDate(-1, 0, 0).Unix()
	for _, p := range all {
		if p.T >= yearAgo {
			if c.High52 == 0 || p.C > c.High52 {
				c.High52 = p.C
			}
			if c.Low52 == 0 || p.C < c.Low52 {
				c.Low52 = p.C
			}
		}
		if p.T >= cutoff {
			c.Points = append(c.Points, p)
		}
	}
	if n := len(c.Points); n > 1500 { // thin long histories to roughly weekly points
		thin := c.Points[:0:0]
		for i, p := range c.Points {
			if i%5 == 0 || i == n-1 {
				thin = append(thin, p)
			}
		}
		c.Points = thin
	}
	return c, nil
}

// ---------------------------------------------------------------- news

type NewsItem struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Source    string `json:"source"`
	Published string `json:"published"` // RFC 3339, UTC
	Summary   string `json:"summary,omitempty"`
	Feed      string `json:"feed"`
}

func parseRSSTime(s string) time.Time {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 02 Jan 2006 15:04:05 Z", time.RFC3339} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func parseRSS(body []byte, feed string) ([]NewsItem, error) {
	var doc struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				PubDate     string `xml:"pubDate"`
				Description string `xml:"description"`
				Source      struct {
					Name string `xml:",chardata"`
				} `xml:"source"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var out []NewsItem
	for _, it := range doc.Channel.Items {
		title := strings.TrimSpace(html.UnescapeString(it.Title))
		src := strings.TrimSpace(it.Source.Name)
		link := strings.TrimSpace(it.Link)
		if title == "" || !(strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://")) {
			continue
		}
		if src != "" { // Google appends " - Publisher" to headlines
			title = strings.TrimSuffix(title, " - "+src)
		}
		n := NewsItem{Title: title, URL: link, Source: src, Feed: feed}
		if t := parseRSSTime(it.PubDate); !t.IsZero() {
			n.Published = t.UTC().Format(time.RFC3339)
		}
		// Google's description only repeats the headline and publisher.
		if sum := cellText(html.UnescapeString(it.Description)); sum != "" && feed != "Google News" && !strings.HasPrefix(sum, title) && sum != src {
			if r := []rune(sum); len(r) > 280 {
				sum = string(r[:277]) + "…"
			}
			n.Summary = sum
		}
		if n.Source == "" {
			if u, err := url.Parse(link); err == nil {
				n.Source = strings.TrimPrefix(u.Hostname(), "www.")
			}
			if n.Source == "finance.yahoo.com" {
				n.Source = "Yahoo Finance"
			}
		}
		out = append(out, n)
	}
	return out, nil
}

var (
	reCorpSuffix  = regexp.MustCompile(`(?i)[,.]?\s+(inc|incorporated|corp|corporation|co|company|ltd|limited|plc|n\.?v|s\.?a|ag|llc|lp|holdings?)\.?$`)
	reStateSuffix = regexp.MustCompile(`\s*/\s*[A-Za-z]{2,3}\s*/?$`)
	reNonWord     = regexp.MustCompile(`[^a-z0-9]+`)
)

// cleanCompanyName turns EDGAR's "Stoke Therapeutics, Inc." into the name people
// search for, "Stoke Therapeutics".
func cleanCompanyName(name string) string {
	n := strings.TrimSpace(reStateSuffix.ReplaceAllString(strings.TrimSpace(name), ""))
	for i := 0; i < 3; i++ {
		n = strings.TrimSpace(reCorpSuffix.ReplaceAllString(n, ""))
	}
	if n = strings.TrimRight(n, ",. "); n == "" {
		return name
	}
	return n
}

// News merges both feeds, newest first, dropping repeated headlines. The string
// slice lists feeds that failed.
func (m *Market) News(ctx context.Context, ticker, name string, fresh bool) ([]NewsItem, []string, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	clean := cleanCompanyName(name)
	ttl := 10 * time.Minute
	if fresh {
		ttl = 0
	}
	type feed struct{ name, url string }
	var feeds []feed
	if clean != "" {
		q := `"` + clean + `"`
		if ticker != "" {
			q += " OR " + ticker + " stock"
		}
		feeds = append(feeds, feed{"Google News", fmt.Sprintf("%s/rss/search?q=%s&hl=en-US&gl=US&ceid=US:en", strings.TrimRight(m.GNews, "/"), url.QueryEscape(q))})
	}
	if reTicker.MatchString(ticker) {
		feeds = append(feeds, feed{"Yahoo Finance", fmt.Sprintf("%s/rss/2.0/headline?s=%s&region=US&lang=en-US", strings.TrimRight(m.YNews, "/"), url.QueryEscape(ticker))})
	}
	type result struct {
		feed  string
		items []NewsItem
		err   error
	}
	ch := make(chan result, len(feeds))
	for _, f := range feeds {
		go func() {
			body, err := m.get(ctx, f.url, ttl)
			var items []NewsItem
			if err == nil {
				items, err = parseRSS(body, f.name)
			}
			ch <- result{f.name, items, err}
		}()
	}
	var all []NewsItem
	problems := []string{}
	for range feeds {
		r := <-ch
		if r.err != nil {
			logf("news %s: %v", r.feed, r.err)
			problems = append(problems, r.feed+" couldn't be reached")
			continue
		}
		all = append(all, r.items...)
	}
	if len(feeds) > 0 && len(problems) == len(feeds) {
		return nil, problems, errors.New("couldn't load news right now. Check your internet connection and try again")
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Published > all[j].Published })
	seen := map[string]bool{}
	out := []NewsItem{}
	for _, it := range all {
		k := reNonWord.ReplaceAllString(strings.ToLower(it.Title), "")
		if len(k) > 60 {
			k = k[:60]
		}
		if !seen[k] && len(out) < 150 {
			seen[k] = true
			out = append(out, it)
			m.allowLinks(it.URL)
		}
	}
	return out, problems, nil
}
