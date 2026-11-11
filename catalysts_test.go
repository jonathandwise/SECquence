package main

import (
	"strings"
	"testing"
	"time"
)

func TestFindPeriods(t *testing.T) {
	cases := map[string][]string{
		"report topline data in the second half of 2027":         {"2H 2027|2027-07-01|2027-12-31"},
		"initiate the study in 1H 2027 and report data in 2H'28": {"1H 2027|2027-01-01|2027-06-30", "2H 2028|2028-07-01|2028-12-31"},
		"expected in the third quarter of 2027":                  {"Q3 2027|2027-07-01|2027-09-30"},
		"in Q4 2026":                                             {"Q4 2026|2026-10-01|2026-12-31"},
		"a PDUFA target action date of March 15, 2027":           {"Mar 15, 2027|2027-03-15|2027-03-15"},
		"by mid-2027":        {"Mid-2027|2027-05-01|2027-08-31"},
		"by the end of 2027": {"YE 2027|2027-10-01|2027-12-31"},
		"by year-end 2026":   {"YE 2026|2026-10-01|2026-12-31"},
		"in early 2028":      {"Early 2028|2028-01-01|2028-04-30"},
		"in June 2027":       {"Jun 2027|2027-06-01|2027-06-30"},
		"sufficient to fund operations into 2029":           {"2029|2029-01-01|2029-12-31"},
		"later this year":                                   {"2026|2026-01-01|2026-12-31"},
		"by the end of the year":                            {"YE 2026|2026-10-01|2026-12-31"},
		"early next year":                                   {"Early 2027|2027-01-01|2027-04-30"},
		"no timeframe here, just 25 patients and 3 cohorts": {},
	}
	for in, want := range cases {
		got := findPeriods(in, 2026)
		var g []string
		for _, p := range got {
			g = append(g, p.Label+"|"+p.Start+"|"+p.End)
		}
		if strings.Join(g, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v want %v", in, g, want)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	s := splitSentences("Stoke Therapeutics, Inc. announced results. The U.S. FDA granted Breakthrough designation in Jan. 2025. We expect data in 2H 2027. Dr. Smith said so.")
	if len(s) != 4 || !strings.HasPrefix(s[0], "Stoke Therapeutics, Inc. announced") || s[2] != "We expect data in 2H 2027." {
		t.Fatalf("%q", s)
	}
}

func TestExtractGuidance(t *testing.T) {
	doc := `<html><body><div style="display:none"><ix:header>We expect data in 2030.</ix:header></div>
	<p>Stoke expects to report topline data from the Phase 3 EMPEROR study of zorevunersen (STK-001) in the second half of 2027. The Company also plans to submit an NDA to the FDA in the first half of 2028.</p>
	<p>We anticipate initiating a Phase 1 study of STK-002 in mid-2027.</p>
	<p>We believe our existing cash, cash equivalents and marketable securities will be sufficient to fund our operations into 2029.</p>
	<p>We expect research and development expenses to increase in 2027 as compared to 2026.</p>
	<p>In January 2025, we completed enrollment in the Phase 2 study of zorevunersen.</p>
	<p>This press release contains forward-looking statements, including our expectation to report data in 2027, and actual results may differ.</p>
	<table><tr><td>Expected data</td><td>2H 2027</td><td>Phase 3</td><td>study</td><td>x</td></tr></table>
	</body></html>`
	g := extractGuidance(doc, DocRef{Form: "8-K", Doc: "EX-99.1"}, 2026)
	var got []string
	for _, x := range g {
		got = append(got, x.Kind+" | "+x.Periods[0].Label+" | "+strings.Join(x.Programs, ","))
	}
	want := []string{
		"Data readout | 2H 2027 | zorevunersen,STK-001",
		"Regulatory | 1H 2028 | ",
		"Trial start / enrollment | Mid-2027 | STK-002",
		"Cash runway | 2029 | ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseCTGov(t *testing.T) {
	body := []byte(`{"studies":[
	 {"protocolSection":{"identificationModule":{"nctId":"NCT06872125","briefTitle":"Study of Zorevunersen in Dravet Syndrome","acronym":"EMPEROR"},
	  "statusModule":{"overallStatus":"RECRUITING","startDateStruct":{"date":"2025-06","type":"ACTUAL"},"primaryCompletionDateStruct":{"date":"2027-09","type":"ESTIMATED"},"completionDateStruct":{"date":"2028-03-31","type":"ESTIMATED"},"lastUpdatePostDateStruct":{"date":"2026-10-01"}},
	  "sponsorCollaboratorsModule":{"leadSponsor":{"name":"Stoke Therapeutics, Inc","class":"INDUSTRY"},"collaborators":[{"name":"Biogen"}]},
	  "conditionsModule":{"conditions":["Dravet Syndrome"]},
	  "designModule":{"phases":["PHASE3"],"enrollmentInfo":{"count":150,"type":"ESTIMATED"}},
	  "armsInterventionsModule":{"interventions":[{"type":"DRUG","name":"Zorevunersen"},{"type":"OTHER","name":"Sham procedure"},{"type":"DRUG","name":"Placebo"}]}}},
	 {"protocolSection":{"identificationModule":{"nctId":"NCT00000001","briefTitle":"Unrelated"},"statusModule":{"overallStatus":"COMPLETED"},
	  "sponsorCollaboratorsModule":{"leadSponsor":{"name":"Stokes University Hospital"}}}}
	],"nextPageToken":"abc"}`)
	ts, next, err := parseCTGov(body, "Stoke Therapeutics")
	if err != nil || next != "abc" || len(ts) != 1 {
		t.Fatalf("%v %q %d", err, next, len(ts))
	}
	tr := ts[0]
	if tr.NCT != "NCT06872125" || tr.PrimaryComplete != "2027-09" || tr.PrimaryType != "ESTIMATED" || tr.Phases[0] != "PHASE3" || tr.Enrollment != 150 ||
		len(tr.Interventions) != 2 || tr.URL != "https://clinicaltrials.gov/study/NCT06872125" {
		t.Fatalf("%+v", tr)
	}
}

func TestEstimateEvents(t *testing.T) {
	today := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	filings := []Filing{
		{Form: "10-Q", Filed: "2026-11-05", Period: "2026-09-30"},
		{Form: "10-Q", Filed: "2026-08-06", Period: "2026-06-30"},
		{Form: "8-K", Filed: "2026-06-05", Period: "2026-06-04", Items: "5.07"},
		{Form: "10-Q", Filed: "2026-05-07", Period: "2026-03-31"},
		{Form: "10-K", Filed: "2026-03-03", Period: "2025-12-31", Acc: "k"},
		{Form: "10-Q", Filed: "2025-11-06", Period: "2025-09-30"},
	}
	ev := estimateEvents(filings, today)
	if len(ev) != 2 || ev[0].Title != "FY 2026 results and 10-K" || ev[0].Date != "2027-03-02" || ev[1].Kind != "Annual meeting" || ev[1].Date != "2027-06-03" {
		t.Fatalf("%+v", ev)
	}
}
