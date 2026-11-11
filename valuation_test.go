package main

import (
	"archive/zip"
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// worksheetModel reproduces the inputs of the "Biotech sum-of-the-parts" worksheet.
func worksheetModel() Model {
	costs := func(p Product, reps float64) Product {
		p.COGS, p.SMPct, p.GAPct, p.PreLaunchReps, p.CostPerRep = 0.1, 0.25, 0.15, reps, 250000
		p.Capex0, p.CapexGrowth, p.DAPct, p.NWC0, p.NWCGrowth, p.Share, p.PNDA = 5, 0.05, 0.95, 5, 0.05, 1, 0.95
		return p
	}
	return Model{ValuationYear: 2019, Horizon: 19, Cash: 300, TaxRate: 0.25, TaxLosses: true, RiskFree: 0.0225, Beta: 1.6, MRP: 0.055, UnallocGA: 10, UnallocGAGrowth: 0.01,
		Products: []Product{
			costs(Product{Name: "Product 1", Launch: 2023, YearsToPeak: 7, GrowthAfterPeak: 0.02, PatentExpiry: 2033, DeclineAfterLOE: -0.5, Patients: 200000, Price: 100000, Penetration: 0.15,
				Phases: [3]Phase{{Done: true}, {Done: true}, {Start: 2019, Enrollment: 400, CostPerPatient: 1e6, Months: 36, PEnroll: 1, PSuccess: 0.65}}}, 100),
			costs(Product{Name: "Product 2", NWCFixed: true, Launch: 2025, YearsToPeak: 5, GrowthAfterPeak: 0.02, PatentExpiry: 2033, DeclineAfterLOE: -0.5, Patients: 200000, Price: 50000, Penetration: 0.2,
				Phases: [3]Phase{{Done: true}, {Start: 2019, Enrollment: 150, CostPerPatient: 750000, Months: 24, PEnroll: 1, PSuccess: 0.35}, {Start: 2021, Enrollment: 300, CostPerPatient: 1e6, Months: 36, PEnroll: 1, PSuccess: 0.65}}}, 40),
			costs(Product{Name: "Product 3", NWCFixed: true, Launch: 2026, YearsToPeak: 7, GrowthAfterPeak: 0.02, PatentExpiry: 2035, DeclineAfterLOE: -0.35, Patients: 1000000, Price: 15000, Penetration: 0.2,
				Phases: [3]Phase{{Start: 2019, Enrollment: 50, CostPerPatient: 500000, Months: 12, PEnroll: 1, PSuccess: 0.7}, {Start: 2020, Enrollment: 200, CostPerPatient: 500000, Months: 24, PEnroll: 1, PSuccess: 0.35}, {Start: 2022, Enrollment: 500, CostPerPatient: 750000, Months: 36, PEnroll: 1, PSuccess: 0.65}}}, 40),
		}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6*math.Max(1, math.Abs(b)) }

// The engine must reproduce the worksheet's own results to the last digit.
func TestValuationMatchesWorksheet(t *testing.T) {
	m := worksheetModel()
	r := m.Calculate()
	if !near(r.CostOfEquity, 0.1105) || !near(r.DiscountRate, 0.11) {
		t.Fatalf("rates %v %v", r.CostOfEquity, r.DiscountRate)
	}
	wantNPV := []float64{1730.671145806098, 201.35965440888214, 179.0467902177167}
	wantPOS := []float64{0.6175, 0.216125, 0.1512875}
	for i, p := range r.Products {
		if !near(p.NPV, wantNPV[i]) || !near(p.POS, wantPOS[i]) {
			t.Errorf("%s: NPV %v (want %v), POS %v (want %v)", p.Name, p.NPV, wantNPV[i], p.POS, wantPOS[i])
		}
	}
	if !near(r.Unallocated.NPV, -65.87447124341227) || !near(r.EquityValue, 2345.2031191892843) {
		t.Errorf("unallocated %v equity %v", r.Unallocated.NPV, r.EquityValue)
	}
	// Spot checks against individual worksheet cells (product 1).
	rows := r.Products[0].Rows
	for _, c := range []struct {
		row  string
		t    int
		want float64
	}{{"uptake", 4, 0.09}, {"uptake", 11, 1.02}, {"uptake", 15, 0.541216}, {"adjRevenue", 4, 166.725}, {"rd", 0, 133.333333}, {"sm", 2, 25}, {"sm", 4, 41.68125},
		{"nwc", 5, -14.82}, {"nwc", 11, -5}, {"fcf", 0, -105.25}, {"fcf", 18, 41.395}, {"df", 0, 0.949158}} {
		if got := rows[c.row][c.t]; math.Abs(got-c.want) > 0.001 {
			t.Errorf("%s[%d] = %v, want %v", c.row, c.t, got, c.want)
		}
	}
	if s := r.Sensitivity; len(s.Rates) != 5 || !near(s.Values[2][2], r.EquityValue) || s.Values[2][0] <= s.Values[2][4] || s.Values[0][2] >= s.Values[4][2] {
		t.Errorf("sensitivity %+v", s)
	}
	// Options beyond the worksheet.
	m.Shares = 50
	m.TaxLosses = false
	m.Products[0].Share = 0.5
	r2 := m.Calculate()
	if !near(r2.PerShare, r2.EquityValue/50) || r2.Products[0].NPV >= r.Products[0].NPV || r2.Unallocated.NPV >= r.Unallocated.NPV {
		t.Errorf("options: %v %v %v", r2.PerShare, r2.Products[0].NPV, r2.Unallocated.NPV)
	}
}

func TestDefaultModel(t *testing.T) {
	s := newMockSEC(t)
	fin, err := s.Financials(context.Background(), 1623526)
	if err != nil {
		t.Fatal(err)
	}
	trials := []Trial{
		{NCT: "1", Status: "RECRUITING", Phases: []string{"PHASE3"}, Interventions: []string{"Zorevunersen"}, Conditions: []string{"Dravet Syndrome"}},
		{NCT: "2", Status: "ACTIVE_NOT_RECRUITING", Phases: []string{"PHASE1", "PHASE2"}, Interventions: []string{"zorevunersen"}},
		{NCT: "3", Status: "RECRUITING", Phases: []string{"PHASE1"}, Interventions: []string{"STK-002"}, Conditions: []string{"ADOA"}},
		{NCT: "4", Status: "COMPLETED", Phases: []string{"PHASE2"}, Interventions: []string{"Old drug"}},
	}
	m := DefaultModel(fin, trials, 2026)
	if m.Cash != 143 || m.Shares != 56.2 || len(m.Products) != 2 || !m.Placeholder {
		t.Fatalf("cash %v shares %v products %d", m.Cash, m.Shares, len(m.Products))
	}
	p := m.Products[0]
	if p.Name != "Zorevunersen" || p.Indication != "Dravet Syndrome" || !p.Phases[0].Done || !p.Phases[1].Done || p.Phases[2].Done || p.Phases[2].Start != 2026 || p.Launch != 2030 {
		t.Fatalf("%+v", p)
	}
	if q := m.Products[1]; q.Name != "STK-002" || q.Phases[0].Done || q.Phases[1].Start != 2027 || q.Phases[2].Start != 2029 || q.Launch != 2033 {
		t.Fatalf("%+v", q)
	}
	if r := m.Calculate(); r.PerShare == 0 || len(r.Years) != 20 {
		t.Fatalf("%+v", r)
	}
	if e := DefaultModel(nil, nil, 2026); len(e.Products) != 1 {
		t.Fatal("expected one starter drug")
	}
}

// The exported workbook's formulas, recalculated by LibreOffice, must give the same
// answers as the engine. Runs only where LibreOffice is installed.
func TestValuationWorkbook(t *testing.T) {
	m := worksheetModel()
	m.Shares = 50
	m.Products[1].Share = 0.6
	r := m.Calculate()
	data, err := valuationXLSX(&m, r, "Example Co", "EXMP", 30, []SourceRow{{Section: "Company", Label: "Beta", Value: "1.6", Start: "1.6", Status: "Starting value", Note: "5-year weekly vs XBI"}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) != 10 {
		t.Fatalf("zip: %v, %d parts", err, len(zr.File))
	}
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		t.Skip("LibreOffice not installed")
	}
	// Strip the cached results so every number must come from the formulas.
	var stripped bytes.Buffer
	zw := zip.NewWriter(&stripped)
	for _, f := range zr.File {
		rc, _ := f.Open()
		var part bytes.Buffer
		part.ReadFrom(rc)
		rc.Close()
		w, _ := zw.Create(f.Name)
		w.Write(regexp.MustCompile(`</f><v>[^<]*</v>`).ReplaceAll(part.Bytes(), []byte("</f>")))
	}
	zw.Close()
	dir := t.TempDir()
	src := filepath.Join(dir, "model.xlsx")
	os.WriteFile(src, stripped.Bytes(), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, soffice, "--headless", "--convert-to", "csv:Text - txt - csv (StarCalc):44,34,76,1,,0,false,true,false,false,false,1", "--outdir", dir, src)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("LibreOffice failed: %v %s", err, out)
	}
	csv, err := os.ReadFile(filepath.Join(dir, "model-Summary.csv"))
	if err != nil {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		t.Skipf("no Summary csv: %v", files)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(csv), "\n") {
		if parts := strings.SplitN(line, ",", 2); len(parts) == 2 {
			got[strings.Trim(parts[0], `"`)] = parts[1]
		}
	}
	t.Logf("recalculated summary: %v", got)
	want := map[string]float64{"Product 1": r.Products[0].NPV, "Product 2": r.Products[1].NPV, "Product 3": r.Products[2].NPV,
		"Unallocated costs": r.Unallocated.NPV, "Equity value": r.EquityValue, "Value per share": r.PerShare}
	for label, w := range want {
		cell := strings.NewReplacer(`"`, "", "$", "", "(", "-", ")", "", " ", "").Replace(strings.Split(got[label]+",", ",")[0])
		v := 0.0
		if _, err := fmtSscan(cell, &v); err != nil || math.Abs(v-w) > 0.06 {
			t.Errorf("%s: workbook gives %q, engine %v", label, got[label], w)
		}
	}
}

func TestInsiders(t *testing.T) {
	s := newMockSEC(t)
	ins, err := s.Insiders(context.Background(), 1623526, 60)
	if err != nil {
		t.Fatal(err)
	}
	if ins.Total != 3 || ins.Scanned != 3 || len(ins.Txns) != 5 || len(ins.Problems) != 0 {
		t.Fatalf("total %d scanned %d txns %d %v", ins.Total, ins.Scanned, len(ins.Txns), ins.Problems)
	}
	buy := ins.Txns[0]
	if buy.Owner != "Kaye Edward M" || buy.Role != "Chief Executive Officer, Director" || buy.Code != "P" || buy.Kind != "Open-market purchase" || !buy.Acquired ||
		buy.Shares != 10000 || buy.Value != 95000 || buy.After != 260000 || buy.Filed != "2025-08-19" || buy.Date != "2025-08-15" {
		t.Fatalf("%+v", buy)
	}
	if sale := ins.Txns[1]; sale.Code != "S" || !sale.Planned || sale.Acquired || sale.Role != "Chief Financial Officer" {
		t.Fatalf("%+v", sale)
	}
	derivs := 0
	for _, x := range ins.Txns {
		if x.Derivative {
			derivs++
		}
	}
	if derivs != 1 {
		t.Fatalf("derivative count %d", derivs)
	}
}

func TestStoreMigrationAndModels(t *testing.T) {
	legacy, dir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(legacy, "settings.json"), []byte(`{"email":"a@b.co","downloadDir":"/x/SEC Filings","exhibits":"all","watchlist":[{"ticker":"STOK","cik":1623526,"name":"Stoke"}]}`), 0o644)
	st := OpenStore(dir, legacy)
	if s := st.Get(); s.Email != "a@b.co" || s.Exhibits != "all" || len(s.Watchlist) != 1 || s.Theme != "light" {
		t.Fatalf("%+v", s)
	}
	if err := st.Update(func(s *Settings) { s.Theme = "dark" }); err != nil {
		t.Fatal(err)
	}
	if s := OpenStore(dir, legacy).Get(); s.Theme != "dark" || s.Email != "a@b.co" {
		t.Fatalf("reload %+v", s)
	}
	m := worksheetModel()
	if _, ok := st.LoadModel(7); ok {
		t.Fatal("unexpected model")
	}
	if err := st.SaveModel(7, &m); err != nil {
		t.Fatal(err)
	}
	if got, ok := st.LoadModel(7); !ok || len(got.Products) != 3 || got.Products[2].Phases[2].PSuccess != 0.65 {
		t.Fatalf("%+v", got)
	}
	st.DeleteModel(7)
	if _, ok := st.LoadModel(7); ok {
		t.Fatal("model not deleted")
	}
}

func fmtSscan(s string, v *float64) (int, error) {
	f, err := strconvParse(s)
	*v = f
	return 1, err
}

func strconvParse(s string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
}

func TestStartingValues(t *testing.T) {
	m := DefaultModel(nil, nil, 2026)
	if m.Base["beta"] != 1.6 || m.Base["taxLosses"] != 1 || m.Base["valuationYear"] != 2026 {
		t.Errorf("company starting values: %v", m.Base)
	}
	if _, has := m.Base["products.0.patients"]; has {
		t.Errorf("company starting values should not include drugs: %v", m.Base)
	}
	p := m.Products[0]
	if p.Base["patients"] != 10000 || p.Base["phases.2.pSuccess"] != 0.65 || p.Base["phases.0.done"] != 0 || p.Base["nwcFixed"] != 0 {
		t.Errorf("drug starting values: %v", p.Base)
	}
	// A model saved by an earlier version gets the standard starting values, except
	// for inputs that came from the filings or the pipeline.
	old := Model{ValuationYear: 2026, Products: []Product{templateProduct("X", 2026)}}
	old.assumeStart()
	if old.Base["beta"] != 1.6 || old.Products[0].Base["price"] != 100000 {
		t.Errorf("assumed starting values missing: %v %v", old.Base, old.Products[0].Base)
	}
	for _, k := range []string{"cash", "shares"} {
		if _, has := old.Base[k]; has {
			t.Errorf("%s should have no assumed starting value", k)
		}
	}
	if _, has := old.Products[0].Base["launch"]; has {
		t.Error("launch should have no assumed starting value")
	}
	src := map[string]string{"a": "  note  ", "b": "   "}
	trimNotes(src)
	if src["a"] != "note" || len(src) != 1 {
		t.Errorf("trimNotes: %v", src)
	}
}
