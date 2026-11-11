package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Risk-adjusted NPV, sum-of-the-parts valuation.
//
// The method follows the "Biotech sum-of-the-parts" worksheet: each drug's peak
// revenue (patients x price x penetration) is spread over an uptake curve, multiplied
// by its probability of reaching approval, and reduced by costs, tax, capex and
// working capital to a free cash flow. Cash flows are discounted mid-year at a
// CAPM cost of equity. Equity value = drug NPVs + unallocated costs + cash - debt.
//
// Two additions to the worksheet, both off by default: an economic interest below
// 100% for partnered drugs, and the choice not to book tax credits on losses.

type Phase struct {
	Done           bool    `json:"done"`
	Start          int     `json:"start"`          // year the trial starts
	Enrollment     float64 `json:"enrollment"`     // patients
	CostPerPatient float64 `json:"costPerPatient"` // USD
	Months         float64 `json:"months"`
	PEnroll        float64 `json:"pEnroll"`  // chance the trial runs; used when the previous phase is done
	PSuccess       float64 `json:"pSuccess"` // chance the trial succeeds
}

type Product struct {
	Name            string   `json:"name"`
	Indication      string   `json:"indication"`
	Launch          int      `json:"launch"`
	YearsToPeak     int      `json:"yearsToPeak"`
	GrowthAfterPeak float64  `json:"growthAfterPeak"`
	PatentExpiry    int      `json:"patentExpiry"`
	DeclineAfterLOE float64  `json:"declineAfterLOE"` // e.g. -0.5
	Patients        float64  `json:"patients"`
	Price           float64  `json:"price"` // USD per patient per year
	Penetration     float64  `json:"penetration"`
	PNDA            float64  `json:"pNDA"`
	Share           float64  `json:"share"` // economic interest, 1 = wholly owned
	Phases          [3]Phase `json:"phases"`
	COGS            float64  `json:"cogs"`  // share of revenue
	SMPct           float64  `json:"smPct"` // selling & marketing after launch, share of revenue
	GAPct           float64  `json:"gaPct"`
	PreLaunchReps   float64  `json:"preLaunchReps"`
	CostPerRep      float64  `json:"costPerRep"` // USD per year
	Capex0          float64  `json:"capex0"`     // $mm in the first year
	CapexGrowth     float64  `json:"capexGrowth"`
	DAPct           float64  `json:"daPct"` // D&A as a share of capex
	NWC0            float64  `json:"nwc0"`  // $mm
	NWCGrowth       float64  `json:"nwcGrowth"`
	// The worksheet treats working capital two ways. Its first drug invests
	// max(revenue growth x NWCGrowth, NWC0) each year; its other drugs simply grow the
	// first year's investment by NWCGrowth a year. NWCFixed selects the second.
	NWCFixed bool `json:"nwcFixed"`
	// Base holds the value each input started with, keyed like the interface's fields
	// ("patients", "phases.0.start"), so an edited input can be told from an untouched
	// one. Src holds the user's source note for an input, keyed the same way.
	Base map[string]float64 `json:"base,omitempty"`
	Src  map[string]string  `json:"src,omitempty"`
}

type Model struct {
	// Name tells a company's saved versions apart ("Base case", "Bull case").
	Name             string    `json:"name,omitempty"`
	ValuationYear    int       `json:"valuationYear"`
	Horizon          int       `json:"horizon"` // years modelled
	Cash             float64   `json:"cash"`    // $mm
	Debt             float64   `json:"debt"`    // $mm
	Shares           float64   `json:"shares"`  // millions
	TaxRate          float64   `json:"taxRate"`
	TaxLosses        bool      `json:"taxLosses"` // book a tax credit on losses, as the worksheet does
	RiskFree         float64   `json:"riskFree"`
	Beta             float64   `json:"beta"`
	MRP              float64   `json:"mrp"`
	DiscountOverride float64   `json:"discountOverride"` // 0 = use the CAPM rate
	UnallocGA        float64   `json:"unallocGA"`        // $mm in the first year
	UnallocGAGrowth  float64   `json:"unallocGAGrowth"`
	Products         []Product `json:"products"`
	Notes            string    `json:"notes"`
	Updated          string    `json:"updated,omitempty"`
	Placeholder      bool      `json:"placeholder,omitempty"` // still holds the starting assumptions
	// Base and Src are the company-level counterparts of Product.Base and Product.Src.
	Base map[string]float64 `json:"base,omitempty"`
	Src  map[string]string  `json:"src,omitempty"`
}

// flatNumbers lists every number and yes/no setting in v by its JSON path.
func flatNumbers(v any) map[string]float64 {
	b, _ := json.Marshal(v)
	var tree any
	_ = json.Unmarshal(b, &tree)
	out := map[string]float64{}
	var walk func(prefix string, x any)
	walk = func(prefix string, x any) {
		switch t := x.(type) {
		case map[string]any:
			for k, c := range t {
				if k == "base" || k == "src" || k == "products" || k == "placeholder" {
					continue
				}
				walk(strings.TrimPrefix(prefix+"."+k, "."), c)
			}
		case []any:
			for i, c := range t {
				walk(prefix+"."+strconv.Itoa(i), c)
			}
		case float64:
			out[prefix] = t
		case bool:
			if out[prefix] = 0; t {
				out[prefix] = 1
			}
		}
	}
	walk("", tree)
	return out
}

// markStart records the model's current inputs as its starting values.
func (m *Model) markStart() {
	m.Base = flatNumbers(m)
	for i := range m.Products {
		m.Products[i].Base = flatNumbers(&m.Products[i])
	}
}

// assumeStart gives a model saved before starting values were recorded the
// worksheet's standard ones. Inputs that were filled in from the filings or the
// pipeline (cash, shares, dates, phase status) are left without one, as what they
// started as is no longer known.
func (m *Model) assumeStart() {
	if m.Base == nil {
		d := DefaultModel(nil, nil, m.ValuationYear)
		m.Base = flatNumbers(&d)
		for _, k := range []string{"valuationYear", "cash", "debt", "shares"} {
			delete(m.Base, k)
		}
	}
	for i := range m.Products {
		if p := &m.Products[i]; p.Base == nil {
			t := templateProduct("", m.ValuationYear)
			p.Base = flatNumbers(&t)
			for _, k := range []string{"launch", "patentExpiry"} {
				delete(p.Base, k)
			}
			for k := 0; k < 3; k++ {
				delete(p.Base, fmt.Sprintf("phases.%d.start", k))
				delete(p.Base, fmt.Sprintf("phases.%d.done", k))
			}
		}
	}
}

// uptakeCurves gives the share of peak sales reached in each year after launch, by
// years to peak (from the worksheet's "Uptake curves" tab).
var uptakeCurves = map[int][]float64{
	3:  {0.31, 0.76, 1},
	4:  {0.21, 0.58, 0.83, 1},
	5:  {0.15, 0.42, 0.68, 0.86, 1},
	6:  {0.11, 0.31, 0.58, 0.76, 0.89, 1},
	7:  {0.09, 0.25, 0.46, 0.66, 0.8, 0.9, 1},
	8:  {0.08, 0.21, 0.37, 0.58, 0.71, 0.83, 0.92, 1},
	9:  {0.07, 0.18, 0.31, 0.49, 0.64, 0.76, 0.85, 0.93, 1},
	10: {0.06, 0.15, 0.28, 0.42, 0.58, 0.68, 0.79, 0.86, 0.93, 1},
}

func clampPeak(years int) int { return max(3, min(10, years)) }

// uptakeAt is the curve value n years after launch (n = 1 in the launch year).
func uptakeAt(yearsToPeak, n int) float64 {
	c := uptakeCurves[clampPeak(yearsToPeak)]
	switch {
	case n < 1:
		return 0
	case n > len(c):
		return 1
	}
	return c[n-1]
}

// probabilities chains the phases: a trial runs with the cumulative probability of
// the phase before it (or its own PEnroll if that phase is done).
func (p *Product) probabilities() (enroll, cum [3]float64, pos float64) {
	for k := range p.Phases {
		ph := p.Phases[k]
		enroll[k] = ph.PEnroll
		if k > 0 && !p.Phases[k-1].Done {
			enroll[k] = cum[k-1]
		}
		cum[k] = enroll[k] * ph.PSuccess
		if ph.Done {
			cum[k] = 1
		}
	}
	return enroll, cum, p.PNDA * cum[2]
}

func (p *Product) peakRevenue() float64 { return p.Patients * p.Price * p.Penetration / 1e6 }

func (m *Model) costOfEquity() float64 { return m.RiskFree + m.Beta*m.MRP }

// discountRate is the CAPM cost of equity rounded to the nearest 0.5%, unless overridden.
func (m *Model) discountRate() float64 {
	if m.DiscountOverride > 0 {
		return m.DiscountOverride
	}
	return math.Round(m.costOfEquity()/0.005) * 0.005
}

type ProductResult struct {
	Name        string               `json:"name"`
	PeakRevenue float64              `json:"peakRevenue"`
	POS         float64              `json:"pos"`
	NPV         float64              `json:"npv"`
	Rows        map[string][]float64 `json:"rows"`
}

type Sensitivity struct {
	Rates    []float64   `json:"rates"`
	Scales   []float64   `json:"scales"`
	Values   [][]float64 `json:"values"` // [scale][rate]
	PerShare bool        `json:"perShare"`
}

type Result struct {
	Years        []int           `json:"years"`
	CostOfEquity float64         `json:"costOfEquity"`
	DiscountRate float64         `json:"discountRate"`
	Products     []ProductResult `json:"products"`
	Unallocated  ProductResult   `json:"unallocated"`
	SumOfParts   float64         `json:"sumOfParts"`
	EquityValue  float64         `json:"equityValue"`
	PerShare     float64         `json:"perShare"`
	Sensitivity  *Sensitivity    `json:"sensitivity,omitempty"`
}

func (m *Model) horizon() int { return max(5, min(40, m.Horizon)) }

func (m *Model) taxOn(profit float64) float64 {
	if !m.TaxLosses && profit < 0 {
		return 0
	}
	return m.TaxRate * profit
}

func (m *Model) product(p *Product, rate float64) ProductResult {
	n := m.horizon()
	rows := map[string][]float64{}
	for _, k := range []string{"uptakeRaw", "postPeak", "uptake", "revenue", "adjRevenue", "cogs", "rd1", "rd2", "rd3", "rd", "sm", "ga", "opProfit", "tax", "nopat", "capex", "da", "nwc", "fcf", "period", "df", "pv"} {
		rows[k] = make([]float64, n)
	}
	enroll, _, pos := p.probabilities()
	peak := p.peakRevenue()
	share := p.Share
	if share <= 0 {
		share = 1
	}
	res := ProductResult{Name: p.Name, PeakRevenue: peak, POS: pos, Rows: rows}
	for t := 0; t < n; t++ {
		year := m.ValuationYear + t
		raw := uptakeAt(p.YearsToPeak, year-p.Launch+1)
		// After the peak, sales grow at a steady rate until the patent expires, then decline.
		postPeak := 0.0
		if year >= p.Launch+clampPeak(p.YearsToPeak) {
			postPeak = p.GrowthAfterPeak
			if year > p.PatentExpiry {
				postPeak = p.DeclineAfterLOE
			}
		}
		uptake := raw
		if t > 0 && rows["uptakeRaw"][t-1] == 1 {
			uptake = rows["uptake"][t-1] * (1 + postPeak)
		}
		rev := peak * uptake
		adj := rev * pos
		var rd [3]float64
		for k, ph := range p.Phases {
			if !ph.Done && ph.Months > 0 && float64(year) >= float64(ph.Start) && float64(year) < float64(ph.Start)+ph.Months/12 {
				rd[k] = ph.Enrollment * ph.CostPerPatient / ph.Months * 12 / 1e6 * enroll[k]
			}
		}
		sm := p.SMPct * adj
		if year == p.Launch-2 || year == p.Launch-1 {
			sm = p.PreLaunchReps * p.CostPerRep / 1e6
		}
		cogs, ga := p.COGS*adj, p.GAPct*adj
		rdTotal := rd[0] + rd[1] + rd[2]
		op := adj - cogs - rdTotal - sm - ga
		tax := m.taxOn(op)
		capex := -p.Capex0
		nwc := -p.NWC0
		if t > 0 {
			capex = rows["capex"][t-1] * (1 + p.CapexGrowth)
			nwc = -math.Max((adj-rows["adjRevenue"][t-1])*p.NWCGrowth, p.NWC0)
			if p.NWCFixed {
				nwc = rows["nwc"][t-1] * (1 + p.NWCGrowth)
			}
		}
		da := -capex * p.DAPct
		fcf := (op - tax + capex + da + nwc) * share
		period := float64(t) + 0.5
		df := 1 / math.Pow(1+rate, period)
		for k, v := range map[string]float64{"uptakeRaw": raw, "postPeak": postPeak, "uptake": uptake, "revenue": rev, "adjRevenue": adj, "cogs": cogs,
			"rd1": rd[0], "rd2": rd[1], "rd3": rd[2], "rd": rdTotal, "sm": sm, "ga": ga, "opProfit": op, "tax": tax, "nopat": op - tax,
			"capex": capex, "da": da, "nwc": nwc, "fcf": fcf, "period": period, "df": df, "pv": fcf * df} {
			rows[k][t] = v
		}
		res.NPV += fcf * df
	}
	return res
}

func (m *Model) unallocated(rate float64) ProductResult {
	n := m.horizon()
	rows := map[string][]float64{"ga": make([]float64, n), "opProfit": make([]float64, n), "tax": make([]float64, n), "fcf": make([]float64, n), "df": make([]float64, n), "pv": make([]float64, n)}
	res := ProductResult{Name: "Unallocated", Rows: rows}
	ga := m.UnallocGA
	for t := 0; t < n; t++ {
		if t > 0 {
			ga *= 1 + m.UnallocGAGrowth
		}
		tax := m.taxOn(-ga)
		df := 1 / math.Pow(1+rate, float64(t)+0.5)
		rows["ga"][t], rows["opProfit"][t], rows["tax"][t], rows["fcf"][t], rows["df"][t], rows["pv"][t] = ga, -ga, tax, -ga-tax, df, (-ga-tax)*df
		res.NPV += (-ga - tax) * df
	}
	return res
}

// equityAt values the company at a discount rate, with every drug's revenue scaled.
func (m *Model) equityAt(rate, scale float64) float64 {
	total := m.unallocated(rate).NPV + m.Cash - m.Debt
	for i := range m.Products {
		p := m.Products[i]
		p.Penetration *= scale
		total += m.product(&p, rate).NPV
	}
	return total
}

// Calculate runs the model.
func (m *Model) Calculate() *Result {
	rate := m.discountRate()
	r := &Result{CostOfEquity: m.costOfEquity(), DiscountRate: rate, Products: []ProductResult{}}
	for t := 0; t < m.horizon(); t++ {
		r.Years = append(r.Years, m.ValuationYear+t)
	}
	for i := range m.Products {
		pr := m.product(&m.Products[i], rate)
		r.Products = append(r.Products, pr)
		r.SumOfParts += pr.NPV
	}
	r.Unallocated = m.unallocated(rate)
	r.SumOfParts += r.Unallocated.NPV
	r.EquityValue = r.SumOfParts + m.Cash - m.Debt
	if m.Shares > 0 {
		r.PerShare = r.EquityValue / m.Shares
	}
	// Sensitivity: discount rate against peak sales.
	s := &Sensitivity{Scales: []float64{0.5, 0.75, 1, 1.25, 1.5}, PerShare: m.Shares > 0}
	for _, d := range []float64{-0.02, -0.01, 0, 0.01, 0.02} {
		if rate+d > 0.005 {
			s.Rates = append(s.Rates, rate+d)
		}
	}
	for _, sc := range s.Scales {
		row := make([]float64, len(s.Rates))
		for j, rt := range s.Rates {
			if row[j] = m.equityAt(rt, sc); m.Shares > 0 {
				row[j] /= m.Shares
			}
		}
		s.Values = append(s.Values, row)
	}
	r.Sensitivity = s
	return r
}

// ---------------------------------------------------------------- starting point

// templateProduct holds the worksheet's default assumptions.
func templateProduct(name string, year int) Product {
	return Product{
		Name: name, Launch: year + 4, YearsToPeak: 7, GrowthAfterPeak: 0.02, PatentExpiry: year + 14, DeclineAfterLOE: -0.5,
		Patients: 10000, Price: 100000, Penetration: 0.2, PNDA: 0.95, Share: 1,
		Phases: [3]Phase{
			{Start: year, Enrollment: 50, CostPerPatient: 500000, Months: 12, PEnroll: 1, PSuccess: 0.7},
			{Start: year + 1, Enrollment: 150, CostPerPatient: 500000, Months: 24, PEnroll: 1, PSuccess: 0.35},
			{Start: year + 3, Enrollment: 300, CostPerPatient: 750000, Months: 36, PEnroll: 1, PSuccess: 0.65},
		},
		COGS: 0.1, SMPct: 0.25, GAPct: 0.15, PreLaunchReps: 40, CostPerRep: 250000,
		Capex0: 5, CapexGrowth: 0.05, DAPct: 0.95, NWC0: 5, NWCGrowth: 0.05,
	}
}

func phaseRank(phases []string) int {
	rank := 0
	for _, p := range phases {
		switch p {
		case "PHASE1", "EARLY_PHASE1":
			rank = max(rank, 1)
		case "PHASE2":
			rank = max(rank, 2)
		case "PHASE3":
			rank = max(rank, 3)
		}
	}
	return rank
}

// DefaultModel builds a starting model: cash, debt and shares from the latest
// filings, and one drug per active clinical program. Market assumptions are the
// worksheet's placeholders and are meant to be replaced.
func DefaultModel(fin *Financials, trials []Trial, year int) Model {
	m := Model{ValuationYear: year, Horizon: 20, TaxRate: 0.25, TaxLosses: true, RiskFree: 0.0425, Beta: 1.6, MRP: 0.055,
		UnallocGA: 10, UnallocGAGrowth: 0.01, Products: []Product{}, Placeholder: true}
	if fin != nil {
		m.Cash = math.Round(fin.Latest["cashinv"].V/1e5) / 10
		m.Debt = math.Round(fin.Latest["debt"].V/1e5) / 10
		m.Shares = math.Round(fin.Latest["shares"].V/1e4) / 100
	}
	// The most advanced active trial of each drug.
	type prog struct {
		name, indication string
		rank             int
	}
	best := map[string]*prog{}
	for _, t := range trials {
		switch t.Status {
		case "COMPLETED", "TERMINATED", "WITHDRAWN", "SUSPENDED", "UNKNOWN":
			continue
		}
		rank := phaseRank(t.Phases)
		if rank == 0 || len(t.Interventions) == 0 {
			continue
		}
		name := strings.TrimSpace(t.Interventions[0])
		key := strings.ToLower(name)
		if b := best[key]; b == nil || rank > b.rank {
			best[key] = &prog{name: name, rank: rank, indication: strings.Join(t.Conditions[:min(1, len(t.Conditions))], "")}
		}
	}
	var progs []*prog
	for _, p := range best {
		progs = append(progs, p)
	}
	sort.Slice(progs, func(i, j int) bool {
		if progs[i].rank != progs[j].rank {
			return progs[i].rank > progs[j].rank
		}
		return progs[i].name < progs[j].name
	})
	for _, pg := range progs[:min(len(progs), 4)] {
		p := templateProduct(pg.name, year)
		p.Indication = pg.indication
		for k := 0; k < pg.rank-1; k++ {
			p.Phases[k].Done = true
		}
		p.Phases[pg.rank-1].Start = year
		for k := pg.rank; k < 3; k++ {
			p.Phases[k].Start = p.Phases[k-1].Start + int(math.Ceil(p.Phases[k-1].Months/12))
		}
		p.Launch = p.Phases[2].Start + int(math.Ceil(p.Phases[2].Months/12)) + 1
		p.PatentExpiry = p.Launch + 12
		m.Products = append(m.Products, p)
	}
	if len(m.Products) == 0 {
		m.Products = append(m.Products, templateProduct("Product 1", year))
	}
	m.markStart()
	return m
}

// ---------------------------------------------------------------- Excel export

// valuationXLSX writes the model as a workbook with live formulas (inputs in blue),
// laid out like the worksheet: Summary, Assumptions, Model and Uptake curves.
// SourceRow is one line of the assumptions-and-sources list the interface compiles.
type SourceRow struct {
	Section string `json:"section"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Start   string `json:"start"`
	Status  string `json:"status"`
	Note    string `json:"note"`
}

func sourcesSheet(rows []SourceRow) *xlSheet {
	sh := &xlSheet{name: "Sources", widths: []float64{26, 40, 16, 16, 16, 90}}
	sh.add(xs("Assumptions and sources", stTitle))
	sh.add(xs("A record of each input when this workbook was exported. Changing the Assumptions sheet does not update it.", stMuted))
	sh.add()
	sh.add(xs("Section", stHead), xs("Assumption", stHead), xs("Value", stHead), xs("Starting value", stHead), xs("Status", stHead), xs("Source or reasoning", stHead))
	for _, r := range rows {
		sh.add(xs(r.Section, 0), xs(r.Label, 0), xs(r.Value, 0), xs(r.Start, 0), xs(r.Status, 0), xs(r.Note, 0))
	}
	return sh
}

func valuationXLSX(m *Model, r *Result, company, ticker string, price float64, sources []SourceRow) ([]byte, error) {
	n := len(r.Years)
	asm := &xlSheet{name: "Assumptions", freezeCols: 1, widths: []float64{38}}
	gen := map[string]string{} // key -> absolute reference on Assumptions
	asm.add(xs(fmt.Sprintf("%s (%s) assumptions", company, ticker), stTitle))
	asm.add(xs("Blue cells are inputs. Change them and every sheet recalculates.", stMuted))
	asm.add()
	asm.add(xs("General", stHead), xs("", stHead))
	general := func(key, label string, c xlCell) {
		row := asm.add(xs(label, 0), c)
		gen[key] = "Assumptions!$B$" + fmt.Sprint(row+1)
	}
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	general("valYear", "Valuation year", xn(float64(m.ValuationYear), stInYear))
	general("cash", "Cash and investments ($mm)", xn(m.Cash, stInMM))
	general("debt", "Debt ($mm)", xn(m.Debt, stInMM))
	general("shares", "Shares outstanding (mm)", xn(m.Shares, stInMM))
	general("tax", "Tax rate", xn(m.TaxRate, stInPct))
	general("taxLosses", "Tax credit on losses (1 = yes, 0 = no)", xn(b2f(m.TaxLosses), stIn))
	general("rf", "Risk-free rate", xn(m.RiskFree, stInPct))
	general("beta", "Beta", xn(m.Beta, stIn))
	general("mrp", "Market risk premium", xn(m.MRP, stInPct))
	general("coe", "Cost of equity", xf(fmt.Sprintf("%s+%s*%s", gen["rf"], gen["beta"], gen["mrp"]), r.CostOfEquity, stPct))
	if m.DiscountOverride > 0 {
		general("rate", "Applied discount rate (set by hand)", xn(m.DiscountOverride, stInPct))
	} else {
		general("rate", "Applied discount rate (rounded to 0.5%)", xf(fmt.Sprintf("MROUND(%s,0.005)", gen["coe"]), r.DiscountRate, stPct))
	}
	general("unallocGA", "Unallocated G&A ($mm)", xn(m.UnallocGA, stInMM))
	general("unallocG", "Unallocated G&A growth", xn(m.UnallocGAGrowth, stInPct))
	asm.add()

	// One column per drug.
	head := []xlCell{xs("Drugs", stHead)}
	for _, p := range m.Products {
		head = append(head, xs(p.Name, stHead))
		asm.widths = append(asm.widths, 22)
	}
	asm.add(head...)
	prodRow := map[string]int{}
	pref := func(key string, i int) string { return fmt.Sprintf("Assumptions!$%s$%d", colName(1+i), prodRow[key]+1) }
	prow := func(key, label string, cell func(i int, p *Product) xlCell) {
		row := []xlCell{xs(label, 0)}
		prodRow[key] = len(asm.rows)
		for i := range m.Products {
			row = append(row, cell(i, &m.Products[i]))
		}
		asm.add(row...)
	}
	section := func(title string) { asm.add(); asm.add(xs(title, stBold)) }
	in := func(style int, get func(p *Product) float64) func(int, *Product) xlCell {
		return func(_ int, p *Product) xlCell { return xn(get(p), style) }
	}
	prow("indication", "Indication", func(_ int, p *Product) xlCell { return xs(p.Indication, stIn) })
	prow("launch", "Launch year", in(stInYear, func(p *Product) float64 { return float64(p.Launch) }))
	prow("ytp", "Years to peak (3 to 10)", in(stIn, func(p *Product) float64 { return float64(clampPeak(p.YearsToPeak)) }))
	prow("growth", "Annual growth after peak", in(stInPct, func(p *Product) float64 { return p.GrowthAfterPeak }))
	prow("expiry", "Patent expiry", in(stInYear, func(p *Product) float64 { return float64(p.PatentExpiry) }))
	prow("decline", "Annual % decline after patent expiry", in(stInPct, func(p *Product) float64 { return p.DeclineAfterLOE }))
	prow("patients", "Patient population", in(stIn, func(p *Product) float64 { return p.Patients }))
	prow("price", "Price per patient per year ($)", in(stIn, func(p *Product) float64 { return p.Price }))
	prow("penetration", "Peak penetration", in(stInPct, func(p *Product) float64 { return p.Penetration }))
	prow("peak", "Unadjusted peak revenue ($mm)", func(i int, p *Product) xlCell {
		return xf(fmt.Sprintf("%s*%s*%s/1000000", pref("patients", i), pref("price", i), pref("penetration", i)), p.peakRevenue(), stMM)
	})
	prow("pnda", "Probability of approval once filed", in(stInPct, func(p *Product) float64 { return p.PNDA }))
	posRow := len(asm.rows)
	prow("pos", "Probability of success to approval", func(int, *Product) xlCell { return xlCell{} }) // filled in below
	prow("share", "Economic interest", in(stInPct, func(p *Product) float64 {
		if p.Share <= 0 {
			return 1
		}
		return p.Share
	}))
	for k := 0; k < 3; k++ {
		s := fmt.Sprint(k + 1)
		section("Phase " + s)
		prow("done"+s, "Completed (1 = yes, 0 = no)", in(stIn, func(p *Product) float64 { return b2f(p.Phases[k].Done) }))
		prow("start"+s, "Start year", in(stInYear, func(p *Product) float64 { return float64(p.Phases[k].Start) }))
		prow("enr"+s, "Target enrollment (patients)", in(stIn, func(p *Product) float64 { return p.Phases[k].Enrollment }))
		prow("cpp"+s, "Cost per patient ($)", in(stIn, func(p *Product) float64 { return p.Phases[k].CostPerPatient }))
		prow("cost"+s, "Cost of trial ($)", func(i int, p *Product) xlCell {
			return xf(pref("enr"+s, i)+"*"+pref("cpp"+s, i), p.Phases[k].Enrollment*p.Phases[k].CostPerPatient, stNum)
		})
		prow("months"+s, "Time to complete (months)", in(stIn, func(p *Product) float64 { return p.Phases[k].Months }))
		prow("penr"+s, "Probability the trial runs", func(i int, p *Product) xlCell {
			enroll, _, _ := p.probabilities()
			if k == 0 {
				return xn(p.Phases[0].PEnroll, stInPct)
			}
			prev := fmt.Sprint(k)
			return xf(fmt.Sprintf("IF(%s=1,%v,%s)", pref("done"+prev, i), p.Phases[k].PEnroll, pref("cum"+prev, i)), enroll[k], stPct)
		})
		prow("psucc"+s, "Probability of success", in(stInPct, func(p *Product) float64 { return p.Phases[k].PSuccess }))
		prow("cum"+s, "Cumulative probability", func(i int, p *Product) xlCell {
			_, cum, _ := p.probabilities()
			return xf(fmt.Sprintf("IF(%s=1,1,%s*%s)", pref("done"+s, i), pref("penr"+s, i), pref("psucc"+s, i)), cum[k], stPct)
		})
	}
	for i := range m.Products {
		_, _, pos := m.Products[i].probabilities()
		asm.rows[posRow][1+i] = xf(pref("pnda", i)+"*"+pref("cum3", i), pos, stPct)
	}
	section("Costs")
	prow("cogs", "Cost of goods sold, % of revenue", in(stInPct, func(p *Product) float64 { return p.COGS }))
	prow("reps", "Pre-launch sales reps", in(stIn, func(p *Product) float64 { return p.PreLaunchReps }))
	prow("cpr", "Annual cost per rep ($)", in(stIn, func(p *Product) float64 { return p.CostPerRep }))
	prow("prelaunch", "Pre-launch selling cost ($mm, two years before launch)", func(i int, p *Product) xlCell {
		return xf(pref("reps", i)+"*"+pref("cpr", i)+"/1000000", p.PreLaunchReps*p.CostPerRep/1e6, stMM)
	})
	prow("sm", "Selling & marketing after launch, % of revenue", in(stInPct, func(p *Product) float64 { return p.SMPct }))
	prow("ga", "G&A, % of revenue", in(stInPct, func(p *Product) float64 { return p.GAPct }))
	section("Cash flow items")
	prow("capex0", "Initial capex ($mm)", in(stInMM, func(p *Product) float64 { return p.Capex0 }))
	prow("capexG", "Capex growth", in(stInPct, func(p *Product) float64 { return p.CapexGrowth }))
	prow("da", "D&A as % of capex", in(stInPct, func(p *Product) float64 { return p.DAPct }))
	prow("nwc0", "Initial net working capital ($mm)", in(stInMM, func(p *Product) float64 { return p.NWC0 }))
	prow("nwcG", "Net working capital growth", in(stInPct, func(p *Product) float64 { return p.NWCGrowth }))
	prow("nwcFixed", "Working capital grows at a fixed rate (1) or with revenue (0)", in(stIn, func(p *Product) float64 { return b2f(p.NWCFixed) }))

	// Uptake curves: rows for 3..10 years to peak, 21 years across.
	up := &xlSheet{name: "Uptake curves", widths: []float64{16}}
	uh := []xlCell{xs("Years to peak", stHead)}
	for y := 1; y <= 21; y++ {
		uh = append(uh, xn(float64(y), stYearHead))
	}
	up.add(uh...)
	for ytp := 3; ytp <= 10; ytp++ {
		row := []xlCell{xn(float64(ytp), stBold)}
		for y := 1; y <= 21; y++ {
			row = append(row, xn(uptakeAt(ytp, y), stIn))
		}
		up.add(row...)
	}

	// Model: one block of yearly rows per drug.
	mod := &xlSheet{name: "Model", freezeCols: 1, widths: []float64{34}}
	for range r.Years {
		mod.widths = append(mod.widths, 11)
	}
	mod.add(xs("Risk-adjusted cash flows, $mm", stTitle))
	npvRef := []string{}
	type line struct {
		key, label string
		style      int
		f          func(t int, at func(key string, dt int) string) string
	}
	block := func(title string, lines []line, values map[string][]float64, npv float64) {
		mod.add()
		start := len(mod.rows)
		rowOf := map[string]int{"year": start}
		for i, l := range lines {
			rowOf[l.key] = start + 1 + i
		}
		yr := []xlCell{xs(title, stHead)}
		for t := 0; t < n; t++ {
			f := gen["valYear"]
			if t > 0 {
				f = ref(t, start) + "+1"
			}
			yr = append(yr, xf(f, float64(r.Years[t]), stYearHead))
		}
		mod.add(yr...)
		for _, l := range lines {
			row := []xlCell{xs(l.label, 0)}
			for t := 0; t < n; t++ {
				at := func(key string, dt int) string { return ref(1+t+dt, rowOf[key]) }
				row = append(row, xf(l.f(t, at), values[l.key][t], l.style))
			}
			mod.add(row...)
		}
		pv := rowOf["pv"]
		npvRow := mod.add(xs("Net present value", stBold), xf(fmt.Sprintf("SUM(%s:%s)", ref(1, pv), ref(n, pv)), npv, stMMBold))
		npvRef = append(npvRef, "Model!$B$"+fmt.Sprint(npvRow+1))
	}
	taxF := func(op string) string {
		return fmt.Sprintf("%s*IF(%s=1,%s,MAX(%s,0))", gen["tax"], gen["taxLosses"], op, op)
	}
	discount := []line{
		{"period", "Discount period (mid-year)", stEPS, func(_ int, at func(string, int) string) string {
			return at("year", 0) + "-" + gen["valYear"] + "+0.5"
		}},
		{"df", "Discount factor", stFactor, func(_ int, at func(string, int) string) string {
			return fmt.Sprintf("1/(1+%s)^%s", gen["rate"], at("period", 0))
		}},
		{"pv", "Present value of free cash flow", stMMBold, func(_ int, at func(string, int) string) string {
			return at("fcf", 0) + "*" + at("df", 0)
		}},
	}
	for i := range m.Products {
		a := func(key string) string { return pref(key, i) }
		rd := func(k string) line {
			return line{"rd" + k, "Phase " + k + " trial cost", stMM, func(_ int, at func(string, int) string) string {
				y := at("year", 0)
				return fmt.Sprintf("IF(AND(%s<>1,%s>0,%s>=%s,%s<%s+%s/12),%s/%s*12/1000000*%s,0)",
					a("done"+k), a("months"+k), y, a("start"+k), y, a("start"+k), a("months"+k), a("cost"+k), a("months"+k), a("penr"+k))
			}}
		}
		lines := []line{
			{"uptakeRaw", "Uptake curve", stPct, func(_ int, at func(string, int) string) string {
				y := at("year", 0)
				return fmt.Sprintf("IF(%s<%s,0,IF(%s-%s+1>21,1,INDEX('Uptake curves'!$B$2:$V$9,MIN(MAX(%s,3),10)-2,%s-%s+1)))", y, a("launch"), y, a("launch"), a("ytp"), y, a("launch"))
			}},
			{"postPeak", "Growth after peak", stPct, func(_ int, at func(string, int) string) string {
				y := at("year", 0)
				return fmt.Sprintf("IF(%s<%s+MIN(MAX(%s,3),10),0,IF(%s>%s,%s,%s))", y, a("launch"), a("ytp"), y, a("expiry"), a("decline"), a("growth"))
			}},
			{"uptake", "Applied uptake", stPct, func(t int, at func(string, int) string) string {
				if t == 0 {
					return at("uptakeRaw", 0)
				}
				return fmt.Sprintf("IF(%s=1,%s*(1+%s),%s)", at("uptakeRaw", -1), at("uptake", -1), at("postPeak", 0), at("uptakeRaw", 0))
			}},
			{"revenue", "Unadjusted revenue", stMM, func(_ int, at func(string, int) string) string { return a("peak") + "*" + at("uptake", 0) }},
			{"adjRevenue", "Risk-adjusted revenue", stMMBold, func(_ int, at func(string, int) string) string { return at("revenue", 0) + "*" + a("pos") }},
			{"cogs", "Cost of goods sold", stMM, func(_ int, at func(string, int) string) string { return at("adjRevenue", 0) + "*" + a("cogs") }},
			rd("1"), rd("2"), rd("3"),
			{"rd", "R&D expense", stMM, func(_ int, at func(string, int) string) string {
				return at("rd1", 0) + "+" + at("rd2", 0) + "+" + at("rd3", 0)
			}},
			{"sm", "Selling & marketing", stMM, func(_ int, at func(string, int) string) string {
				y := at("year", 0)
				return fmt.Sprintf("IF(OR(%s=%s-2,%s=%s-1),%s,%s*%s)", y, a("launch"), y, a("launch"), a("prelaunch"), a("sm"), at("adjRevenue", 0))
			}},
			{"ga", "G&A", stMM, func(_ int, at func(string, int) string) string { return a("ga") + "*" + at("adjRevenue", 0) }},
			{"opProfit", "Operating profit", stMMBold, func(_ int, at func(string, int) string) string {
				return fmt.Sprintf("%s-%s-%s-%s-%s", at("adjRevenue", 0), at("cogs", 0), at("rd", 0), at("sm", 0), at("ga", 0))
			}},
			{"tax", "Taxes", stMM, func(_ int, at func(string, int) string) string { return taxF(at("opProfit", 0)) }},
			{"nopat", "NOPAT", stMM, func(_ int, at func(string, int) string) string { return at("opProfit", 0) + "-" + at("tax", 0) }},
			{"capex", "(-) Capex", stMM, func(t int, at func(string, int) string) string {
				if t == 0 {
					return "-" + a("capex0")
				}
				return at("capex", -1) + "*(1+" + a("capexG") + ")"
			}},
			{"da", "(+) D&A", stMM, func(_ int, at func(string, int) string) string { return "-" + at("capex", 0) + "*" + a("da") }},
			{"nwc", "(-/+) Change in net working capital", stMM, func(t int, at func(string, int) string) string {
				if t == 0 {
					return "-" + a("nwc0")
				}
				return fmt.Sprintf("IF(%s=1,%s*(1+%s),-MAX((%s-%s)*%s,%s))", a("nwcFixed"), at("nwc", -1), a("nwcG"), at("adjRevenue", 0), at("adjRevenue", -1), a("nwcG"), a("nwc0"))
			}},
			{"fcf", "Free cash flow (x economic interest)", stMMBold, func(_ int, at func(string, int) string) string {
				return fmt.Sprintf("(%s+%s+%s+%s)*%s", at("nopat", 0), at("capex", 0), at("da", 0), at("nwc", 0), a("share"))
			}},
		}
		block(m.Products[i].Name, append(lines, discount...), r.Products[i].Rows, r.Products[i].NPV)
	}
	un := r.Unallocated.Rows
	un["period"] = make([]float64, n)
	for t := range un["period"] {
		un["period"][t] = float64(t) + 0.5
	}
	block("Unallocated", append([]line{
		{"ga", "G&A expense", stMM, func(t int, at func(string, int) string) string {
			if t == 0 {
				return gen["unallocGA"]
			}
			return at("ga", -1) + "*(1+" + gen["unallocG"] + ")"
		}},
		{"opProfit", "Operating profit", stMM, func(_ int, at func(string, int) string) string { return "-" + at("ga", 0) }},
		{"tax", "Taxes", stMM, func(_ int, at func(string, int) string) string { return taxF(at("opProfit", 0)) }},
		{"fcf", "Free cash flow", stMMBold, func(_ int, at func(string, int) string) string { return at("opProfit", 0) + "-" + at("tax", 0) }},
	}, discount...), un, r.Unallocated.NPV)

	// Summary.
	sum := &xlSheet{name: "Summary", widths: []float64{38, 16, 16}}
	sum.add(xs(fmt.Sprintf("%s (%s) sum-of-the-parts valuation", company, ticker), stTitle))
	sum.add(xs("Risk-adjusted NPV. Method follows the Biotech sum-of-the-parts worksheet; built with SECquence.", stMuted))
	sum.add()
	sum.add(xs("$mm", stHead), xs("NPV", stHead), xs("Probability of success", stHead))
	first := len(sum.rows)
	for i, p := range r.Products {
		sum.add(xs(p.Name, 0), xf(npvRef[i], p.NPV, stMM), xf(pref("pos", i), p.POS, stPct))
	}
	sum.add(xs("Unallocated costs", 0), xf(npvRef[len(npvRef)-1], r.Unallocated.NPV, stMM))
	last := len(sum.rows) - 1
	sop := sum.add(xs("Sum of the parts", stBold), xf(fmt.Sprintf("SUM(%s:%s)", ref(1, first), ref(1, last)), r.SumOfParts, stMMBold))
	cash := sum.add(xs("+ Cash and investments", 0), xf(gen["cash"], m.Cash, stMM))
	debt := sum.add(xs("- Debt", 0), xf(gen["debt"], m.Debt, stMM))
	eq := sum.add(xs("Equity value", stBold), xf(fmt.Sprintf("%s+%s-%s", ref(1, sop), ref(1, cash), ref(1, debt)), r.EquityValue, stMMBold))
	sh := sum.add(xs("Shares outstanding (mm)", 0), xf(gen["shares"], m.Shares, stMM))
	ps := sum.add(xs("Value per share", stBold), xf(fmt.Sprintf("IF(%s>0,%s/%s,0)", ref(1, sh), ref(1, eq), ref(1, sh)), r.PerShare, stPrice))
	if price > 0 {
		pr := sum.add(xs("Share price when exported", 0), xn(price, stPrice))
		sum.add(xs("Upside / (downside)", 0), xf(fmt.Sprintf("%s/%s-1", ref(1, ps), ref(1, pr)), r.PerShare/price-1, stPct))
	}
	sum.add()
	sum.add(xs("Discount rate", 0), xf(gen["rate"], r.DiscountRate, stPct))
	if m.Notes != "" {
		sum.add()
		sum.add(xs("Notes", stBold))
		for _, l := range strings.Split(m.Notes, "\n") {
			sum.add(xs(l, 0))
		}
	}
	sheets := []*xlSheet{sum, asm, mod, up}
	if len(sources) > 0 {
		sheets = append(sheets, sourcesSheet(sources))
	}
	return writeXLSX(sheets)
}
