package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Insider transactions, read from the XML inside Form 4 filings.

type InsiderTxn struct {
	Date       string  `json:"date"`
	Filed      string  `json:"filed"`
	Acc        string  `json:"acc"`
	Owner      string  `json:"owner"`
	Role       string  `json:"role"`
	Security   string  `json:"security"`
	Code       string  `json:"code"`
	Kind       string  `json:"kind"`
	Acquired   bool    `json:"acquired"`
	Shares     float64 `json:"shares"`
	Price      float64 `json:"price"`
	Value      float64 `json:"value"`
	After      float64 `json:"after"` // shares held after the transaction
	Derivative bool    `json:"derivative"`
	Planned    bool    `json:"planned"` // under a Rule 10b5-1 trading plan
}

type InsiderTotals struct {
	Count  int     `json:"count"`
	Shares float64 `json:"shares"`
	Value  float64 `json:"value"`
}

type Insiders struct {
	Txns     []InsiderTxn  `json:"txns"`
	Buys     InsiderTotals `json:"buys"`  // open-market purchases, last 12 months
	Sells    InsiderTotals `json:"sells"` // open-market sales, last 12 months
	Scanned  int           `json:"scanned"`
	Total    int           `json:"total"` // Form 4 filings on EDGAR
	Since    string        `json:"since"` // oldest filing read
	Problems []string      `json:"problems"`
}

var txnKinds = map[string]string{
	"P": "Open-market purchase", "S": "Open-market sale", "A": "Grant or award", "M": "Option exercise", "F": "Shares withheld for tax",
	"G": "Gift", "C": "Conversion", "D": "Returned to company", "X": "Option exercise", "J": "Other", "W": "Inheritance", "I": "Discretionary",
	"E": "Short-derivative expiry", "H": "Derivative expiry", "O": "Out-of-the-money exercise", "U": "Tender in change of control", "L": "Small acquisition", "Z": "Voting trust", "K": "Equity swap",
}

type xmlVal struct {
	Value string `xml:"value"`
}

func (v xmlVal) num() float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(v.Value), 64)
	return f
}

type xmlTxn struct {
	Security xmlVal `xml:"securityTitle"`
	Date     xmlVal `xml:"transactionDate"`
	Coding   struct {
		Code string `xml:"transactionCode"`
	} `xml:"transactionCoding"`
	Amounts struct {
		Shares xmlVal `xml:"transactionShares"`
		Price  xmlVal `xml:"transactionPricePerShare"`
		AD     xmlVal `xml:"transactionAcquiredDisposedCode"`
	} `xml:"transactionAmounts"`
	Post struct {
		Shares xmlVal `xml:"sharesOwnedFollowingTransaction"`
	} `xml:"postTransactionAmounts"`
}

type ownershipDoc struct {
	Aff10b5One string `xml:"aff10b5One"`
	Owners     []struct {
		Name string `xml:"reportingOwnerId>rptOwnerName"`
		Rel  struct {
			Director string `xml:"isDirector"`
			Officer  string `xml:"isOfficer"`
			TenPct   string `xml:"isTenPercentOwner"`
			Other    string `xml:"isOther"`
			Title    string `xml:"officerTitle"`
		} `xml:"reportingOwnerRelationship"`
	} `xml:"reportingOwner"`
	NonDerivative []xmlTxn `xml:"nonDerivativeTable>nonDerivativeTransaction"`
	Derivative    []xmlTxn `xml:"derivativeTable>derivativeTransaction"`
}

func isYes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "1" || s == "true"
}

// properName turns EDGAR's "KAYE EDWARD M" into "Kaye Edward M".
func properName(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// parseForm4 returns the transactions reported in one ownership document.
func parseForm4(body []byte) ([]InsiderTxn, error) {
	var d ownershipDoc
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	owner, role := "", ""
	if len(d.Owners) > 0 {
		o := d.Owners[0]
		owner = properName(o.Name)
		if len(d.Owners) > 1 {
			owner += fmt.Sprintf(" and %d other", len(d.Owners)-1)
		}
		var roles []string
		if t := strings.TrimSpace(o.Rel.Title); isYes(o.Rel.Officer) && t != "" {
			roles = append(roles, t)
		} else if isYes(o.Rel.Officer) {
			roles = append(roles, "Officer")
		}
		if isYes(o.Rel.Director) {
			roles = append(roles, "Director")
		}
		if isYes(o.Rel.TenPct) {
			roles = append(roles, "10% owner")
		}
		role = strings.Join(roles, ", ")
	}
	var out []InsiderTxn
	add := func(list []xmlTxn, derivative bool) {
		for _, x := range list {
			code := strings.ToUpper(strings.TrimSpace(x.Coding.Code))
			t := InsiderTxn{Date: strings.TrimSpace(x.Date.Value), Owner: owner, Role: role, Security: strings.TrimSpace(x.Security.Value),
				Code: code, Kind: txnKinds[code], Acquired: strings.EqualFold(strings.TrimSpace(x.Amounts.AD.Value), "A"),
				Shares: x.Amounts.Shares.num(), Price: x.Amounts.Price.num(), After: x.Post.Shares.num(), Derivative: derivative, Planned: isYes(d.Aff10b5One)}
			if len(t.Date) > 10 {
				t.Date = t.Date[:10]
			}
			if t.Kind == "" {
				t.Kind = "Code " + code
			}
			t.Value = t.Shares * t.Price
			out = append(out, t)
		}
	}
	add(d.NonDerivative, false)
	add(d.Derivative, true)
	return out, nil
}

// Insiders reads the company's most recent Form 4 filings (up to limit).
func (s *SEC) Insiders(ctx context.Context, cik, limit int) (*Insiders, error) {
	_, filings, err := s.Filings(ctx, cik)
	if err != nil {
		return nil, err
	}
	var forms []Filing
	for _, f := range filings {
		if f.Form == "4" {
			forms = append(forms, f)
		}
	}
	res := &Insiders{Txns: []InsiderTxn{}, Problems: []string{}, Total: len(forms)}
	forms = forms[:min(len(forms), limit)]
	results := make([][]InsiderTxn, len(forms))
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	slots := make(chan struct{}, 4)
	for i, f := range forms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			// The primary document is an HTML rendering ("xslF345X05/form4.xml"); the
			// data is the same file name without the folder.
			name := path.Base(f.PrimaryDoc)
			if !strings.HasSuffix(strings.ToLower(name), ".xml") {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			body, _, err := s.FetchCached(ctx, s.archiveBase(cik, f.Acc)+"/"+name, 60*24*time.Hour)
			var txns []InsiderTxn
			if err == nil {
				txns, err = parseForm4(body)
			}
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			for j := range txns {
				txns[j].Filed, txns[j].Acc = f.Filed, f.Acc
				if txns[j].Date == "" {
					txns[j].Date = f.Period
				}
			}
			results[i] = txns
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cutoff := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	for i, txns := range results {
		if txns != nil {
			res.Scanned++
			res.Since = forms[i].Filed
		}
		for _, t := range txns {
			res.Txns = append(res.Txns, t)
			if t.Derivative || t.Date < cutoff {
				continue
			}
			switch t.Code {
			case "P":
				res.Buys.Count, res.Buys.Shares, res.Buys.Value = res.Buys.Count+1, res.Buys.Shares+t.Shares, res.Buys.Value+t.Value
			case "S":
				res.Sells.Count, res.Sells.Shares, res.Sells.Value = res.Sells.Count+1, res.Sells.Shares+t.Shares, res.Sells.Value+t.Value
			}
		}
	}
	sort.SliceStable(res.Txns, func(i, j int) bool { return res.Txns[i].Date > res.Txns[j].Date })
	if failed > 0 {
		res.Problems = append(res.Problems, fmt.Sprintf("%d of %d Form 4 filings couldn't be read", failed, len(forms)))
	}
	return res, nil
}

// ---------------------------------------------------------------- peer comparison

type PeerRow struct {
	CIK       int     `json:"cik"`
	Ticker    string  `json:"ticker"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"changePct"`
	High52    float64 `json:"high52"`
	Low52     float64 `json:"low52"`
	MarketCap float64 `json:"marketCap"`
	CashInv   float64 `json:"cashInv"`
	Debt      float64 `json:"debt"`
	EV        float64 `json:"ev"`
	Burn      float64 `json:"burn"` // per quarter
	RunwayQ   float64 `json:"runwayQ"`
	Through   string  `json:"through"`
	Positive  bool    `json:"cashFlowPositive"`
	RD        float64 `json:"rd"` // latest quarter
	AsOf      string  `json:"asOf"`
	HasPrice  bool    `json:"hasPrice"`
	HasFin    bool    `json:"hasFin"`
	Problem   string  `json:"problem,omitempty"`
}

// Peers builds one comparison row per watched company.
func Peers(ctx context.Context, sec *SEC, market *Market, list []WatchItem) []PeerRow {
	rows := make([]PeerRow, len(list))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 3)
	for i, w := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			r := PeerRow{CIK: w.CIK, Ticker: w.Ticker, Name: w.Name}
			var problems []string
			if w.Ticker != "" {
				if q, err := market.Chart(ctx, w.Ticker, "1d"); err == nil {
					r.Price, r.High52, r.Low52, r.HasPrice = q.Price, q.High52, q.Low52, true
					if q.PrevClose > 0 {
						r.ChangePct = (q.Price/q.PrevClose - 1) * 100
					}
				} else {
					problems = append(problems, "no price")
				}
			}
			if fin, err := sec.Financials(ctx, w.CIK); err == nil {
				r.HasFin = true
				r.CashInv, r.Debt, r.RD = fin.Latest["cashinv"].V, fin.Latest["debt"].V, fin.Latest["rd"].V
				r.AsOf = fin.Latest["cashinv"].AsOf
				if rw := fin.Runway; rw != nil {
					r.Burn, r.RunwayQ, r.Through, r.Positive = rw.AvgBurn, rw.Quarters, rw.Through, rw.CashFlowOK
				}
				if sh := fin.Latest["shares"].V; sh > 0 && r.HasPrice {
					r.MarketCap = sh * r.Price
					r.EV = r.MarketCap - r.CashInv + r.Debt
				}
			} else {
				problems = append(problems, "no XBRL financials")
			}
			r.Problem = strings.Join(problems, ", ")
			rows[i] = r
		}()
	}
	wg.Wait()
	return rows
}
