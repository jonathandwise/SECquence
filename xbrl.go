package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Key financials from the SEC "company facts" XBRL API.
//
// Quarterly figures use 3-month facts where the company reported them. Where it only
// reported cumulative figures (Q4, and most cash-flow lines), the quarter is derived
// from year-to-date facts: Q4 = full year - nine months, and so on. Every cell keeps
// its sources: the concept, period and filing of each input, how the inputs were
// combined, and any other filings that reported the same period.

type rawFact struct {
	Start   string  `json:"start"`
	End     string  `json:"end"`
	Val     float64 `json:"val"`
	Filed   string  `json:"filed"`
	Form    string  `json:"form"`
	Accn    string  `json:"accn"`
	Concept string  `json:"-"`
	Unit    string  `json:"-"`
}

type factsJSON struct {
	EntityName string `json:"entityName"`
	Facts      map[string]map[string]struct {
		Units map[string][]rawFact `json:"units"`
	} `json:"facts"`
}

// Src is one reported XBRL value behind a cell.
type Src struct {
	Concept string  `json:"concept"`
	Unit    string  `json:"unit"`
	Start   string  `json:"start,omitempty"`
	End     string  `json:"end"`
	Val     float64 `json:"val"`
	Accn    string  `json:"accn"`
	Form    string  `json:"form"`
	Filed   string  `json:"filed"`
	Role    string  `json:"role,omitempty"`
}

type Cell struct {
	V   float64 `json:"v"`
	D   bool    `json:"d,omitempty"`  // derived rather than reported
	Op  string  `json:"op,omitempty"` // "sub": Src[0]-Src[1]; "sum": total of Src
	Src []Src   `json:"src"`
	Alt []Src   `json:"alt,omitempty"`
}

type Col struct {
	Key   string `json:"key"` // period end date
	Label string `json:"label"`
}

type Row struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Unit    string  `json:"unit"`
	Group   string  `json:"group"`
	Concept string  `json:"concept"`
	Cells   []*Cell `json:"cells"`
}

type Table struct {
	Columns []Col `json:"columns"`
	Rows    []Row `json:"rows"`
}

// newestFirst returns a copy of the table with its periods in reverse order.
func (t Table) newestFirst() Table {
	out := Table{Columns: make([]Col, len(t.Columns)), Rows: make([]Row, len(t.Rows))}
	n := len(t.Columns)
	for i, c := range t.Columns {
		out.Columns[n-1-i] = c
	}
	for ri, r := range t.Rows {
		cells := make([]*Cell, len(r.Cells))
		for i, c := range r.Cells {
			cells[len(r.Cells)-1-i] = c
		}
		r.Cells = cells
		out.Rows[ri] = r
	}
	return out
}

type Runway struct {
	AsOf       string  `json:"asOf"`
	CashInv    float64 `json:"cashInv"`
	AvgBurn    float64 `json:"avgBurn"` // cash used per quarter, as a positive number
	Quarters   float64 `json:"quarters"`
	Through    string  `json:"through"`
	BurnBasis  int     `json:"burnBasis"` // quarters averaged
	CashFlowOK bool    `json:"cashFlowPositive"`
}

type SharePoint struct {
	Date   string  `json:"date"`
	Shares float64 `json:"shares"`
	Form   string  `json:"form"`
	Accn   string  `json:"accn"`
	Filed  string  `json:"filed"`
}

// Latest is the most recent value of a metric and the period it belongs to.
type Latest struct {
	V    float64 `json:"v"`
	AsOf string  `json:"asOf"`
}

type Financials struct {
	CIK          int               `json:"cik"`
	Entity       string            `json:"entity"`
	Quarterly    Table             `json:"quarterly"`
	Annual       Table             `json:"annual"`
	Runway       *Runway           `json:"runway"`
	Latest       map[string]Latest `json:"latest"`
	ShareHistory []SharePoint      `json:"shareHistory"`
}

type metric struct {
	Key, Label, Group string
	Kind              string // flow, instant, ratio (not additive), cover, sum
	Unit              string
	Concepts          []string // first concept with a value for a period wins
}

var metrics = []metric{
	{"revenue", "Total revenue", "Income statement", "flow", "USD", []string{"Revenues", "RevenueFromContractWithCustomerExcludingAssessedTax", "RevenueFromContractWithCustomerIncludingAssessedTax", "RevenueFromCollaborativeArrangementExcludingRevenueFromContractWithCustomer", "RevenuesNetOfInterestExpense"}},
	{"rd", "Research & development", "Income statement", "flow", "USD", []string{"ResearchAndDevelopmentExpense", "ResearchAndDevelopmentExpenseExcludingAcquiredInProcessCost"}},
	{"ga", "General & administrative", "Income statement", "flow", "USD", []string{"GeneralAndAdministrativeExpense", "SellingGeneralAndAdministrativeExpense"}},
	{"opex", "Total operating expenses", "Income statement", "flow", "USD", []string{"OperatingExpenses", "CostsAndExpenses"}},
	{"opinc", "Operating income (loss)", "Income statement", "flow", "USD", []string{"OperatingIncomeLoss"}},
	{"netinc", "Net income (loss)", "Income statement", "flow", "USD", []string{"NetIncomeLoss", "ProfitLoss", "NetIncomeLossAvailableToCommonStockholdersBasic"}},
	{"eps", "EPS (basic)", "Income statement", "ratio", "USD/shares", []string{"EarningsPerShareBasic", "EarningsPerShareBasicAndDiluted"}},
	{"ocf", "Operating cash flow", "Cash flow", "flow", "USD", []string{"NetCashProvidedByUsedInOperatingActivities", "NetCashProvidedByUsedInOperatingActivitiesContinuingOperations"}},
	{"sbc", "Stock-based compensation", "Cash flow", "flow", "USD", []string{"ShareBasedCompensation", "AllocatedShareBasedCompensationExpense"}},
	{"issuance", "Proceeds from stock issuance", "Cash flow", "flow", "USD", []string{"ProceedsFromIssuanceOfCommonStock", "ProceedsFromIssuanceOrSaleOfEquity", "ProceedsFromIssuanceInitialPublicOffering"}},
	{"cash", "Cash & cash equivalents", "Balance sheet", "instant", "USD", []string{"CashAndCashEquivalentsAtCarryingValue", "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", "Cash"}},
	{"sti", "Short-term investments", "Balance sheet", "instant", "USD", []string{"ShortTermInvestments", "MarketableSecuritiesCurrent", "AvailableForSaleSecuritiesDebtSecuritiesCurrent", "AvailableForSaleSecuritiesCurrent", "HeldToMaturitySecuritiesCurrent"}},
	{"lti", "Long-term investments", "Balance sheet", "instant", "USD", []string{"LongTermInvestments", "MarketableSecuritiesNoncurrent", "AvailableForSaleSecuritiesDebtSecuritiesNoncurrent", "AvailableForSaleSecuritiesNoncurrent", "HeldToMaturitySecuritiesNoncurrent"}},
	{"cashinv", "Cash + investments", "Balance sheet", "sum", "USD", []string{"cash", "sti", "lti"}},
	{"assets", "Total assets", "Balance sheet", "instant", "USD", []string{"Assets"}},
	{"debt", "Debt (notes & loans)", "Balance sheet", "instant", "USD", []string{"LongTermDebt", "DebtLongtermAndShorttermCombinedAmount", "LongTermDebtNoncurrent", "ConvertibleNotesPayable", "ConvertibleNotesPayableNoncurrent", "LongTermNotesPayable", "NotesPayable", "SeniorNotes"}},
	{"liab", "Total liabilities", "Balance sheet", "instant", "USD", []string{"Liabilities"}},
	{"equity", "Stockholders' equity", "Balance sheet", "instant", "USD", []string{"StockholdersEquity", "StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest"}},
	{"wshares", "Weighted avg. shares (basic)", "Shares", "ratio", "shares", []string{"WeightedAverageNumberOfSharesOutstandingBasic", "WeightedAverageNumberOfShareOutstandingBasicAndDiluted"}},
	{"shares", "Shares outstanding (cover page)", "Shares", "cover", "shares", []string{"dei:EntityCommonStockSharesOutstanding"}},
}

const coverShares = "dei:EntityCommonStockSharesOutstanding"

func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", s)
	return t, err == nil
}

func daysBetween(a, b string) int {
	ta, ok1 := parseDate(a)
	tb, ok2 := parseDate(b)
	if !ok1 || !ok2 {
		return -1
	}
	return int(math.Round(tb.Sub(ta).Hours() / 24))
}

func isQuarterLen(d int) bool { return d >= 80 && d <= 100 }
func isYearLen(d int) bool    { return d >= 350 && d <= 380 }

func periodRole(start, end string) string {
	if start == "" {
		return "Balance as of " + end
	}
	switch d := daysBetween(start, end); {
	case isQuarterLen(d):
		return "3 months"
	case d >= 170 && d <= 190:
		return "6 months year-to-date"
	case d >= 260 && d <= 280:
		return "9 months year-to-date"
	case isYearLen(d):
		return "Full year"
	default:
		return fmt.Sprintf("%d days", d)
	}
}

func srcOf(f rawFact, role string) Src {
	if role == "" {
		role = periodRole(f.Start, f.End)
	}
	return Src{Concept: f.Concept, Unit: f.Unit, Start: f.Start, End: f.End, Val: f.Val, Accn: f.Accn, Form: f.Form, Filed: f.Filed, Role: role}
}

// pick keeps one fact per (start, end) period: the most recently filed. The second
// result maps each period to the other filings that reported it.
func pick(fs []rawFact) ([]rawFact, map[string][]Src) {
	groups := map[string][]rawFact{}
	for _, f := range fs {
		if _, ok := parseDate(f.End); ok {
			groups[f.Start+"|"+f.End] = append(groups[f.Start+"|"+f.End], f)
		}
	}
	chosen := make([]rawFact, 0, len(groups))
	alts := map[string][]Src{}
	for k, g := range groups {
		sort.SliceStable(g, func(i, j int) bool {
			if g[i].Filed != g[j].Filed {
				return g[i].Filed > g[j].Filed
			}
			return g[i].Accn > g[j].Accn
		})
		chosen = append(chosen, g[0])
		seen := map[string]bool{g[0].Accn: true}
		for _, f := range g[1:] {
			if !seen[f.Accn] && len(alts[k]) < 10 {
				seen[f.Accn] = true
				alts[k] = append(alts[k], srcOf(f, ""))
			}
		}
	}
	sort.Slice(chosen, func(i, j int) bool {
		if chosen[i].End != chosen[j].End {
			return chosen[i].End < chosen[j].End
		}
		return chosen[i].Start < chosen[j].Start
	})
	return chosen, alts
}

type series map[string]*Cell // keyed by period end

func quarterly(fs []rawFact, additive bool) series {
	chosen, alts := pick(fs)
	out := series{}
	filed := map[string]string{}
	for _, f := range chosen {
		if f.Start != "" && isQuarterLen(daysBetween(f.Start, f.End)) && f.Filed >= filed[f.End] {
			out[f.End] = &Cell{V: f.Val, Src: []Src{srcOf(f, "")}, Alt: alts[f.Start+"|"+f.End]}
			filed[f.End] = f.Filed
		}
	}
	if !additive {
		return out
	}
	// Year-to-date facts that share a start date: each step between consecutive
	// periods that is one quarter long gives a derived quarter.
	byStart := map[string][]rawFact{}
	for _, f := range chosen {
		if d := daysBetween(f.Start, f.End); f.Start != "" && d >= 80 && d <= 380 {
			byStart[f.Start] = append(byStart[f.Start], f)
		}
	}
	for _, g := range byStart {
		for i := 1; i < len(g); i++ { // g is already sorted by end date
			a, b := g[i-1], g[i]
			if _, have := out[b.End]; !have && isQuarterLen(daysBetween(a.End, b.End)) {
				out[b.End] = &Cell{V: b.Val - a.Val, D: true, Op: "sub", Src: []Src{srcOf(b, ""), srcOf(a, "")}}
			}
		}
	}
	return out
}

func annual(fs []rawFact) series {
	chosen, alts := pick(fs)
	out := series{}
	for _, f := range chosen {
		if f.Start != "" && isYearLen(daysBetween(f.Start, f.End)) {
			out[f.End] = &Cell{V: f.Val, Src: []Src{srcOf(f, "")}, Alt: alts[f.Start+"|"+f.End]}
		}
	}
	return out
}

func instants(fs []rawFact) series {
	chosen, alts := pick(fs)
	out := series{}
	for _, f := range chosen {
		if f.Start == "" {
			out[f.End] = &Cell{V: f.Val, Src: []Src{srcOf(f, "")}, Alt: alts["|"+f.End]}
		}
	}
	return out
}

func lookup(facts *factsJSON, name, unit string) []rawFact {
	tax, local := "us-gaap", name
	if i := strings.Index(name, ":"); i > 0 {
		tax, local = name[:i], name[i+1:]
	}
	src := facts.Facts[tax][local].Units[unit]
	out := make([]rawFact, len(src))
	for i, f := range src {
		f.Concept, f.Unit = tax+":"+local, unit
		out[i] = f
	}
	return out
}

func metricSeries(facts *factsJSON, m metric, mode string) (series, string) {
	out := series{}
	used := ""
	for _, c := range m.Concepts {
		fs := lookup(facts, c, m.Unit)
		if len(fs) == 0 {
			continue
		}
		var s series
		switch {
		case m.Kind == "instant":
			s = instants(fs)
		case mode == "annual":
			s = annual(fs)
		default:
			s = quarterly(fs, m.Kind == "flow")
		}
		if len(s) > 0 && used == "" {
			used = c
		}
		for k, v := range s {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	return out, used
}

func periodLabel(end, mode string) string {
	t, ok := parseDate(end)
	if !ok {
		return end
	}
	if t.Day() <= 7 { // 52/53-week fiscal years can end in the first days of a month
		t = t.AddDate(0, 0, -t.Day())
	}
	if mode == "annual" {
		return "FY " + t.Format("Jan 2006")
	}
	return "Q/E " + t.Format("Jan 2006")
}

func buildTable(facts *factsJSON, mode string, maxCols int) Table {
	all := map[string]series{}
	concept := map[string]string{}
	label := map[string]string{}
	for _, m := range metrics {
		label[m.Key] = m.Label
		if m.Kind != "sum" && m.Kind != "cover" {
			all[m.Key], concept[m.Key] = metricSeries(facts, m, mode)
		}
	}
	// Columns are the periods for which a core flow metric exists.
	set := map[string]bool{}
	for _, k := range []string{"rd", "netinc", "opex", "ocf", "revenue"} {
		for end := range all[k] {
			set[end] = true
		}
	}
	var keys []string
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kept := keys[:0]
	for i, k := range keys { // drop stub periods that end just before the next one
		if i+1 < len(keys) && daysBetween(k, keys[i+1]) < 20 {
			continue
		}
		kept = append(kept, k)
	}
	keys = kept
	if len(keys) > maxCols {
		keys = keys[len(keys)-maxCols:]
	}
	t := Table{Columns: []Col{}, Rows: []Row{}}
	for _, k := range keys {
		t.Columns = append(t.Columns, Col{Key: k, Label: periodLabel(k, mode)})
	}

	// Cover-page share counts are dated a few weeks after the period end; attach each
	// to the latest period ending on or before it.
	cover, coverFiled := series{}, map[string]string{}
	for _, f := range lookup(facts, coverShares, "shares") {
		best := ""
		for _, k := range keys {
			if d := daysBetween(k, f.End); d >= 0 && d <= 130 {
				best = k
			}
		}
		if best != "" && f.Filed >= coverFiled[best] {
			cover[best] = &Cell{V: f.Val, Src: []Src{srcOf(f, "Cover page, shares outstanding as of "+f.End)}}
			coverFiled[best] = f.Filed
		}
	}
	all["shares"], concept["shares"] = cover, coverShares

	sum := series{}
	for _, k := range keys {
		if _, ok := all["cash"][k]; !ok {
			continue
		}
		c := &Cell{Op: "sum"}
		for _, part := range []string{"cash", "sti", "lti"} {
			if p, ok := all[part][k]; ok {
				c.V += p.V
				for _, s := range p.Src {
					s.Role = label[part]
					c.Src = append(c.Src, s)
				}
			}
		}
		sum[k] = c
	}
	all["cashinv"], concept["cashinv"] = sum, "Cash + short-term + long-term investments"

	for _, m := range metrics {
		row := Row{Key: m.Key, Label: m.Label, Unit: m.Unit, Group: m.Group, Concept: concept[m.Key]}
		any := false
		for _, k := range keys {
			c := all[m.Key][k]
			row.Cells = append(row.Cells, c)
			any = any || c != nil
		}
		if any {
			t.Rows = append(t.Rows, row)
		}
	}
	return t
}

func (t Table) row(key string) *Row {
	for i := range t.Rows {
		if t.Rows[i].Key == key {
			return &t.Rows[i]
		}
	}
	return nil
}

// latest returns the newest non-empty cell of a row.
func (t Table) latest(key string) (*Cell, string) {
	r := t.row(key)
	if r == nil {
		return nil, ""
	}
	for i := len(r.Cells) - 1; i >= 0; i-- {
		if r.Cells[i] != nil {
			return r.Cells[i], t.Columns[i].Key
		}
	}
	return nil, ""
}

// computeRunway divides cash + investments by the average operating cash burn of the
// last four reported quarters.
func computeRunway(t Table) *Runway {
	ci, ocf := t.row("cashinv"), t.row("ocf")
	if ci == nil || ocf == nil {
		return nil
	}
	last := -1
	for i := len(t.Columns) - 1; i >= 0 && last < 0; i-- {
		if ci.Cells[i] != nil {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	var total float64
	n := 0
	for i := last; i >= 0 && n < 4; i-- {
		if ocf.Cells[i] != nil {
			total += ocf.Cells[i].V
			n++
		}
	}
	if n == 0 {
		return nil
	}
	avg := total / float64(n)
	r := &Runway{AsOf: t.Columns[last].Key, CashInv: ci.Cells[last].V, AvgBurn: -avg, BurnBasis: n}
	if avg >= 0 {
		r.CashFlowOK = true
		return r
	}
	r.Quarters = r.CashInv / -avg
	if asOf, ok := parseDate(r.AsOf); ok {
		end := asOf.AddDate(0, 0, int(r.Quarters*91.3))
		r.Through = fmt.Sprintf("Q%d %d", (int(end.Month())-1)/3+1, end.Year())
	}
	return r
}

func shareHistory(facts *factsJSON) []SharePoint {
	byDate := map[string]rawFact{}
	for _, f := range lookup(facts, coverShares, "shares") {
		if _, ok := parseDate(f.End); ok && f.Val > 0 && f.Filed >= byDate[f.End].Filed {
			byDate[f.End] = f
		}
	}
	out := make([]SharePoint, 0, len(byDate))
	for d, f := range byDate {
		out = append(out, SharePoint{Date: d, Shares: f.Val, Form: f.Form, Accn: f.Accn, Filed: f.Filed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func financialsFromJSON(body []byte) (*Financials, error) {
	var facts factsJSON
	if err := json.Unmarshal(body, &facts); err != nil {
		return nil, fmt.Errorf("could not read SEC financial data: %w", err)
	}
	f := &Financials{Entity: facts.EntityName, Latest: map[string]Latest{}}
	f.Quarterly = buildTable(&facts, "quarterly", 12)
	f.Annual = buildTable(&facts, "annual", 8)
	f.Runway = computeRunway(f.Quarterly)
	f.ShareHistory = shareHistory(&facts)
	for _, m := range metrics {
		if c, asOf := f.Quarterly.latest(m.Key); c != nil {
			f.Latest[m.Key] = Latest{c.V, asOf}
		}
	}
	// The newest cover-page share count, even if it is dated after the last period.
	if n := len(f.ShareHistory); n > 0 {
		f.Latest["shares"] = Latest{f.ShareHistory[n-1].Shares, f.ShareHistory[n-1].Date}
	}
	return f, nil
}

func (s *SEC) Financials(ctx context.Context, cik int) (*Financials, error) {
	body, _, err := s.FetchCached(ctx, fmt.Sprintf("%s/api/xbrl/companyfacts/CIK%010d.json", s.Data, cik), 30*time.Minute)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("the SEC has no XBRL financial data for this company (common for funds, very new filers and some foreign filers)")
		}
		return nil, err
	}
	f, err := financialsFromJSON(body)
	if err == nil {
		f.CIK = cik
	}
	return f, err
}

func (t Table) CSV() string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	head := []string{"Metric", "Unit"}
	for _, c := range t.Columns {
		head = append(head, c.Key)
	}
	_ = w.Write(head)
	for _, r := range t.Rows {
		rec := []string{r.Label, r.Unit}
		for _, c := range r.Cells {
			if c == nil {
				rec = append(rec, "")
			} else {
				rec = append(rec, strconv.FormatFloat(c.V, 'f', -1, 64))
			}
		}
		_ = w.Write(rec)
	}
	_ = w.Write([]string{})
	_ = w.Write([]string{"Source: SEC EDGAR XBRL company facts. Some quarterly values are calculated from year-to-date figures; the Excel export lists the source of every cell."})
	w.Flush()
	return b.String()
}

// howComputed describes a cell's calculation in words.
func howComputed(c *Cell) string {
	switch {
	case c.Op == "sub" && len(c.Src) == 2:
		return fmt.Sprintf("Calculated: %s minus %s", strings.ToLower(c.Src[0].Role), strings.ToLower(c.Src[1].Role))
	case c.Op == "sum":
		return "Calculated: sum of components"
	}
	return "Reported"
}
