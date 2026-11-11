package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A small .xlsx writer: text, numbers, formulas (with cached results so the file
// shows values before Excel recalculates), number formats, frozen panes and column
// widths. No dependencies beyond the standard library.

type xlCell struct {
	text    string
	num     float64
	kind    byte // 0 empty, 's' text, 'n' number, 'f' formula -> number, 't' formula -> text
	formula string
	style   int
}

func xs(s string, style int) xlCell  { return xlCell{text: s, kind: 's', style: style} }
func xn(n float64, style int) xlCell { return xlCell{num: n, kind: 'n', style: style} }
func xblank(style int) xlCell        { return xlCell{style: style} }
func xf(formula string, cachedValue float64, style int) xlCell {
	return xlCell{formula: formula, num: cachedValue, kind: 'f', style: style}
}
func xlink(url, label string) xlCell {
	q := func(s string) string { return strings.ReplaceAll(s, `"`, `""`) }
	return xlCell{formula: fmt.Sprintf(`HYPERLINK("%s","%s")`, q(url), q(label)), text: label, kind: 't', style: stLink}
}

// Cell styles. The order here is the style index used in cells.
const (
	stDefault = iota
	stBold
	stNum      // #,##0 with negatives in parentheses
	stNumItal  // ... italic (a calculated value)
	stEPS      // 0.00
	stEPSItal  //
	stLink     //
	stItal     //
	stTitle    //
	stHead     // bold on a light fill
	stMM       // #,##0.0 ($ millions)
	stMMBold   //
	stPct      // 0.0%
	stYearHead // year, bold on a light fill
	stFactor   // 0.000
	stIn       // an input: blue text
	stInPct    //
	stInYear   //
	stInMM     //
	stPrice    // $0.00 bold
	stMuted    // grey italic note
)

var xlStyleDefs = [...]struct{ font, fill, numFmt int }{
	stDefault: {0, 0, 0}, stBold: {1, 0, 0}, stNum: {0, 0, 164}, stNumItal: {2, 0, 164}, stEPS: {0, 0, 165}, stEPSItal: {2, 0, 165},
	stLink: {3, 0, 0}, stItal: {2, 0, 0}, stTitle: {4, 0, 0}, stHead: {1, 2, 0}, stMM: {0, 0, 166}, stMMBold: {1, 0, 166},
	stPct: {0, 0, 167}, stYearHead: {1, 2, 168}, stFactor: {0, 0, 169}, stIn: {5, 0, 0}, stInPct: {5, 0, 167}, stInYear: {5, 0, 168},
	stInMM: {5, 0, 166}, stPrice: {1, 0, 170}, stMuted: {6, 0, 0},
}

func xlStylesXML() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="7"><numFmt numFmtId="164" formatCode="#,##0;(#,##0)"/><numFmt numFmtId="165" formatCode="0.00;(0.00)"/><numFmt numFmtId="166" formatCode="#,##0.0;(#,##0.0)"/><numFmt numFmtId="167" formatCode="0.0%"/><numFmt numFmtId="168" formatCode="0"/><numFmt numFmtId="169" formatCode="0.000"/><numFmt numFmtId="170" formatCode="&quot;$&quot;#,##0.00;(&quot;$&quot;#,##0.00)"/></numFmts>
<fonts count="7">
<font><sz val="11"/><name val="Calibri"/><family val="2"/></font>
<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font>
<font><i/><sz val="11"/><name val="Calibri"/><family val="2"/></font>
<font><u/><sz val="11"/><color rgb="FF0563C1"/><name val="Calibri"/><family val="2"/></font>
<font><b/><sz val="14"/><name val="Calibri"/><family val="2"/></font>
<font><sz val="11"/><color rgb="FF0000FF"/><name val="Calibri"/><family val="2"/></font>
<font><i/><sz val="11"/><color rgb="FF6B7280"/><name val="Calibri"/><family val="2"/></font>
</fonts>
<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFE9EEF0"/><bgColor indexed="64"/></patternFill></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
`)
	fmt.Fprintf(&b, `<cellXfs count="%d">`, len(xlStyleDefs))
	for _, d := range xlStyleDefs {
		fmt.Fprintf(&b, `<xf numFmtId="%d" fontId="%d" fillId="%d" borderId="0" xfId="0" applyNumberFormat="1" applyFont="1" applyFill="1"/>`, d.numFmt, d.font, d.fill)
	}
	b.WriteString(`</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`)
	return b.String()
}

func xmlEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r >= 0x20 || r == '\t' || r == '\n':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// colName turns a zero-based column number into letters: 0 -> A, 27 -> AB.
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// ref is an A1 reference from zero-based column and row.
func ref(col, row int) string { return colName(col) + strconv.Itoa(row+1) }

type xlSheet struct {
	name       string
	rows       [][]xlCell
	widths     []float64
	freezeCols int
	freezeRows int
}

// add appends a row and returns its zero-based index.
func (sh *xlSheet) add(cells ...xlCell) int {
	sh.rows = append(sh.rows, cells)
	return len(sh.rows) - 1
}

func (sh *xlSheet) xml() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if sh.freezeCols > 0 || sh.freezeRows > 0 {
		pane := `<pane`
		active := "bottomLeft"
		if sh.freezeCols > 0 {
			pane += fmt.Sprintf(` xSplit="%d"`, sh.freezeCols)
			active = "topRight"
		}
		if sh.freezeRows > 0 {
			pane += fmt.Sprintf(` ySplit="%d"`, sh.freezeRows)
			if sh.freezeCols > 0 {
				active = "bottomRight"
			} else {
				active = "bottomLeft"
			}
		}
		fmt.Fprintf(&b, `<sheetViews><sheetView workbookViewId="0">%s topLeftCell="%s" activePane="%s" state="frozen"/></sheetView></sheetViews>`,
			pane, ref(sh.freezeCols, sh.freezeRows), active)
	}
	if len(sh.widths) > 0 {
		b.WriteString("<cols>")
		for i, w := range sh.widths {
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, i+1, i+1, w)
		}
		b.WriteString("</cols>")
	}
	b.WriteString("<sheetData>")
	for r, row := range sh.rows {
		fmt.Fprintf(&b, `<row r="%d">`, r+1)
		for c, cell := range row {
			at := fmt.Sprintf(`r="%s"`, ref(c, r))
			if cell.style != 0 {
				at += fmt.Sprintf(` s="%d"`, cell.style)
			}
			num := strconv.FormatFloat(cell.num, 'f', -1, 64)
			switch cell.kind {
			case 's':
				if cell.text != "" {
					fmt.Fprintf(&b, `<c %s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, at, xmlEsc(cell.text))
				}
			case 'n':
				fmt.Fprintf(&b, `<c %s><v>%s</v></c>`, at, num)
			case 'f':
				fmt.Fprintf(&b, `<c %s><f>%s</f><v>%s</v></c>`, at, xmlEsc(cell.formula), num)
			case 't':
				fmt.Fprintf(&b, `<c %s t="str"><f>%s</f><v>%s</v></c>`, at, xmlEsc(cell.formula), xmlEsc(cell.text))
			default:
				if cell.style != 0 {
					fmt.Fprintf(&b, `<c %s/>`, at)
				}
			}
		}
		b.WriteString("</row>")
	}
	b.WriteString("</sheetData></worksheet>")
	return b.String()
}

func writeXLSX(sheets []*xlSheet) ([]byte, error) {
	var types, list, rels strings.Builder
	for i, sh := range sheets {
		n := i + 1
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&list, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEsc(sh.name), n, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
	}
	const head = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`
	parts := [][2]string{
		{"[Content_Types].xml", head + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` + types.String() + `</Types>`},
		{"_rels/.rels", head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", head + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView/></bookViews><sheets>` + list.String() + `</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`},
		{"xl/_rels/workbook.xml.rels", head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, len(sheets)+1)},
		{"xl/styles.xml", xlStylesXML()},
	}
	for i, sh := range sheets {
		parts = append(parts, [2]string{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), sh.xml()})
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := z.CreateHeader(&zip.FileHeader{Name: p[0], Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- key financials export

func filingIndexURL(www string, cik int, accn string) string {
	if www == "" {
		www = "https://www.sec.gov"
	}
	return fmt.Sprintf("%s/Archives/edgar/data/%d/%s/%s-index.htm", www, cik, strings.ReplaceAll(accn, "-", ""), accn)
}

// financialsXLSX writes the table plus a Sources sheet with one row for every input
// behind every number.
func financialsXLSX(f *Financials, t Table, mode, ticker, www string) ([]byte, error) {
	fin := &xlSheet{name: "Financials", freezeCols: 2, freezeRows: 3, widths: []float64{34, 11}}
	fin.add(xs(fmt.Sprintf("%s (%s) key financials, %s", f.Entity, ticker, mode), stTitle))
	fin.add(xs("Values in US dollars (EPS in $ per share, shares in units). Italic = calculated from year-to-date figures. The Sources sheet lists every input.", stItal))
	head := []xlCell{xs("Metric", stHead), xs("Unit", stHead)}
	for _, c := range t.Columns {
		head = append(head, xs(c.Key, stHead))
		fin.widths = append(fin.widths, 15)
	}
	fin.add(head...)
	numStyle := func(unit string, derived bool) int {
		st := stNum
		if unit == "USD/shares" {
			st = stEPS
		}
		if derived {
			st++ // the italic variant follows the plain one
		}
		return st
	}
	group := ""
	for _, r := range t.Rows {
		if r.Group != group {
			group = r.Group
			fin.add(xs(group, stBold))
		}
		row := []xlCell{xs(r.Label, 0), xs(r.Unit, 0)}
		for _, c := range r.Cells {
			if c == nil {
				row = append(row, xlCell{})
			} else {
				row = append(row, xn(c.V, numStyle(r.Unit, c.D)))
			}
		}
		fin.add(row...)
	}

	src := &xlSheet{name: "Sources", freezeRows: 1, widths: []float64{30, 12, 16, 40, 26, 16, 52, 25, 9, 11, 22, 13, 60}}
	var titles []xlCell
	for _, h := range []string{"Metric", "Period end", "Value in table", "How computed", "Input", "Input value", "XBRL concept", "Input period", "Form", "Filed", "Accession no.", "Filing", "Also reported in"} {
		titles = append(titles, xs(h, stHead))
	}
	src.add(titles...)
	for _, r := range t.Rows {
		for ci, c := range r.Cells {
			if c == nil {
				continue
			}
			st := numStyle(r.Unit, false)
			var also []string
			for _, a := range c.Alt {
				also = append(also, fmt.Sprintf("%s filed %s (%s): %s", a.Form, a.Filed, a.Accn, strconv.FormatFloat(a.Val, 'f', -1, 64)))
			}
			for si, s := range c.Src {
				period, input := s.End, s.Role
				if s.Start != "" {
					period = s.Start + " to " + s.End
				}
				if c.Op == "sub" && si == 1 {
					input = "minus " + s.Role
				}
				row := []xlCell{xs(r.Label, 0), xs(t.Columns[ci].Key, 0), {}, {}}
				if si == 0 {
					row[2], row[3] = xn(c.V, st), xs(howComputed(c), 0)
				}
				row = append(row, xs(input, 0), xn(s.Val, st), xs(s.Concept, 0), xs(period, 0), xs(s.Form, 0), xs(s.Filed, 0), xs(s.Accn, 0),
					xlink(filingIndexURL(www, f.CIK, s.Accn), "Open filing"))
				if si == 0 && len(also) > 0 {
					row = append(row, xs(strings.Join(also, "; "), 0))
				}
				src.add(row...)
			}
		}
	}
	src.add()
	src.add(xs(`Source: SEC EDGAR XBRL company facts (data.sec.gov). When several filings report the same period, the most recently filed value is used; the others are listed under "Also reported in".`, stItal))
	return writeXLSX([]*xlSheet{fin, src})
}
