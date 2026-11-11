"""Builds a mock EDGAR tree (www.sec.gov + data.sec.gov layouts) for offline testing.
All numbers are synthetic test data."""
import json, os, sys, datetime as dt

root = sys.argv[1]
www = os.path.join(root, "www")
data = os.path.join(root, "data")
CIK = 1623526
def w(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    mode = "wb" if isinstance(content, bytes) else "w"
    with open(path, mode) as f:
        f.write(content)

w(f"{www}/files/company_tickers.json", json.dumps({
    "0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
    "1": {"cik_str": 1623526, "ticker": "STOK", "title": "Stoke Therapeutics, Inc."},
    "2": {"cik_str": 1000001, "ticker": "STOKX", "title": "Stokely Example Corp"},
    "3": {"cik_str": 1000002, "ticker": "SRPT", "title": "Sarepta Therapeutics, Inc."},
}))

# ---------------------------------------------------------------- filings
recent = []
def acc(n, yy): return f"{CIK:010d}-{yy:02d}-{n:06d}"
seq = 100
for year in [2025, 2024, 2023]:
    for (form, period, filed, items, doc) in [
        ("10-Q", f"{year}-09-30", f"{year}-11-06", "", f"stok-{year}0930.htm"),
        ("8-K", f"{year}-11-06", f"{year}-11-06", "2.02,9.01", f"stok-{year}1106.htm"),
        ("4", f"{year}-08-15", f"{year}-08-19", "", "xslF345X05/wk-form4_1.xml"),
        ("10-Q", f"{year}-06-30", f"{year}-08-07", "", f"stok-{year}0630.htm"),
        ("8-K", f"{year}-06-02", f"{year}-06-02", "7.01,8.01,9.01", f"stok-{year}0602.htm"),
        ("DEF 14A", f"{year}-04-20", f"{year}-04-20", "", f"def14a{year}.htm"),
        ("10-Q", f"{year}-03-31", f"{year}-05-08", "", f"stok-{year}0331.htm"),
        ("10-K", f"{year-1}-12-31", f"{year}-03-04", "", f"stok-{year-1}1231.htm"),
    ]:
        if year == 2025 and filed > "2025-11-06":
            continue
        seq -= 1
        recent.append(dict(accessionNumber=acc(seq, year % 100), filingDate=filed, reportDate=period,
                           acceptanceDateTime=filed + "T16:05:00.000Z", form=form, items=items,
                           size=123456, primaryDocument=doc, primaryDocDescription=form))
recent.sort(key=lambda r: r["filingDate"], reverse=True)
cols = {k: [r[k] for r in recent] for k in recent[0]}
cols["isXBRL"] = [1] * len(recent)
cols["isInlineXBRL"] = [1] * len(recent)
old = [dict(accessionNumber="0001193125-19-140001", filingDate="2019-06-18", reportDate="", acceptanceDateTime="2019-06-18T17:00:00.000Z",
            form="S-1", items="", size=4000000, primaryDocument="d123s1.htm", primaryDocDescription="S-1")]
oldcols = {k: [r[k] for r in old] for k in old[0]}
w(f"{data}/submissions/CIK{CIK:010d}.json", json.dumps({
    "cik": str(CIK), "entityType": "operating", "sic": "2834", "sicDescription": "Pharmaceutical Preparations",
    "name": "Stoke Therapeutics, Inc.", "tickers": ["STOK"], "exchanges": ["Nasdaq"], "fiscalYearEnd": "1231",
    "stateOfIncorporation": "DE", "category": "Accelerated filer",
    "filings": {"recent": cols, "files": [{"name": f"CIK{CIK:010d}-submissions-001.json", "filingCount": 1,
                                             "filingFrom": "2019-06-18", "filingTo": "2019-06-18"}]}}))
w(f"{data}/submissions/CIK{CIK:010d}-submissions-001.json", json.dumps(oldcols))

# ---------------------------------------------------------------- documents
def index_page(a, rows):
    trs = []
    for i, (seqn, desc, href, name, typ, size) in enumerate(rows):
        ix = '&nbsp;&nbsp;<span class="label label-info">iXBRL</span>' if href.startswith("/ix?doc=") else ""
        trs.append(f'''         <tr{' class="evenRow"' if i % 2 else ''}>
            <td scope="row">{seqn}</td>
            <td scope="row">{desc}</td>
            <td scope="row"><a href="{href}">{name}</a>{ix}</td>
            <td scope="row">{typ}</td>
            <td scope="row">{size}</td>
         </tr>''')
    return f'''<!DOCTYPE html><html><head><title>EDGAR Filing Documents for {a}</title></head><body>
<div id="formDiv"><div style="padding: 0px 0px 4px 0px; font-size: 12px; margin: 0px 2px 0px 5px; width: 100%; overflow:hidden">
<p>Document Format Files</p>
<table class="tableFile" summary="Document Format Files">
         <tr>
            <th scope="col" style="width: 5%;"><acronym title="Sequence Number">Seq</acronym></th>
            <th scope="col" style="width: 40%;">Description</th>
            <th scope="col" style="width: 20%;">Document</th>
            <th scope="col" style="width: 10%;">Type</th>
            <th scope="col">Size</th>
         </tr>
{chr(10).join(trs)}
      </table>
<p>Data Files</p>
<table class="tableFile" summary="Data Files">
         <tr><th scope="col">Seq</th><th scope="col">Description</th><th scope="col">Document</th><th scope="col">Type</th><th scope="col">Size</th></tr>
         <tr><td scope="row">5</td><td scope="row">XBRL TAXONOMY EXTENSION SCHEMA DOCUMENT</td><td scope="row"><a href="/Archives/edgar/data/{CIK}/{a.replace('-','')}/stok-20250930.xsd">stok-20250930.xsd</a></td><td scope="row">EX-101.SCH</td><td scope="row">41234</td></tr>
      </table></div></div></body></html>'''

GUIDANCE = """<h3>Recent Developments and Anticipated Milestones</h3>
<p>Stoke expects to report topline data from the Phase 3 EMPEROR study of zorevunersen (STK-001) in Dravet syndrome in the second half of 2027.</p>
<p>The Company plans to submit a New Drug Application (NDA) for zorevunersen to the FDA in the first half of 2028, subject to the EMPEROR results.</p>
<p>We anticipate initiating a Phase 1 study of STK-002 for autosomal dominant optic atrophy (ADOA) in mid-2027.</p>
<p>Two-year data from the open-label extension studies are expected to be presented at the American Epilepsy Society meeting in December 2026.</p>
<p>We believe our existing cash, cash equivalents and marketable securities will be sufficient to fund our operations into 2029.</p>
<p>We expect research and development expenses to increase in 2027 compared with 2026.</p>
<p>In January 2025, we completed enrollment in the Phase 2 MONARCH study.</p>
<p>This document contains forward-looking statements, including expectations for data in 2027; actual results may differ materially.</p>"""

def doc_html(title, body, img=None):
    im = f'<p><img src="{img}" alt="logo" style="width:120px"></p>' if img else ""
    paras = "".join(f"<p>{body} Paragraph {i}. Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.</p>" for i in range(1, 40))
    return f'''<html><head><meta http-equiv="Content-Type" content="text/html"><title>{title}</title></head>
<body style="font-family:Times New Roman"><div style="display:none"><ix:header>hidden xbrl header</ix:header></div>
{im}<h2 style="text-align:center">UNITED STATES<br>SECURITIES AND EXCHANGE COMMISSION</h2><h3 style="text-align:center">{title}</h3>
<table border="1" style="border-collapse:collapse"><tr><th>Item</th><th>Three months ended</th></tr>
<tr><td>Research and development</td><td>$ 24,512</td></tr><tr><td>Net loss</td><td>$ (28,901)</td></tr></table>{paras}</body></html>'''

def form4_xml(r):
    y = int(r["filingDate"][:4]); d = r["reportDate"]
    def txn(tag, code, shares, price, ad, after, title="Common Stock"):
        return f"<{tag}><securityTitle><value>{title}</value></securityTitle><transactionDate><value>{d}</value></transactionDate><transactionCoding><transactionFormType>4</transactionFormType><transactionCode>{code}</transactionCode></transactionCoding><transactionAmounts><transactionShares><value>{shares}</value></transactionShares><transactionPricePerShare><value>{price}</value></transactionPricePerShare><transactionAcquiredDisposedCode><value>{ad}</value></transactionAcquiredDisposedCode></transactionAmounts><postTransactionAmounts><sharesOwnedFollowingTransaction><value>{after}</value></sharesOwnedFollowingTransaction></postTransactionAmounts></{tag}>"
    if y == 2025:
        owner, rel, plan = "KAYE EDWARD M", "<isDirector>1</isDirector><isOfficer>1</isOfficer><officerTitle>Chief Executive Officer</officerTitle>", 0
        nd, dv = txn("nonDerivativeTransaction", "P", 10000, 9.5, "A", 260000), ""
    elif y == 2024:
        owner, rel, plan = "TULIPANO STEPHEN J", "<isDirector>0</isDirector><isOfficer>true</isOfficer><officerTitle>Chief Financial Officer</officerTitle>", 1
        nd, dv = txn("nonDerivativeTransaction", "S", 5000, 12.0, "D", 41000), ""
    else:
        owner, rel, plan = "SMITH JANE", "<isDirector>1</isDirector><isOfficer>0</isOfficer>", 0
        nd = txn("nonDerivativeTransaction", "M", 2000, 3.1, "A", 12000) + txn("nonDerivativeTransaction", "F", 700, 8.0, "D", 11300)
        dv = txn("derivativeTransaction", "M", 2000, 0, "D", 8000, "Stock Option (Right to Buy)")
    return f"""<?xml version="1.0"?><ownershipDocument><schemaVersion>X0508</schemaVersion><documentType>4</documentType><periodOfReport>{d}</periodOfReport><aff10b5One>{plan}</aff10b5One>
<issuer><issuerCik>{CIK:010d}</issuerCik><issuerName>Stoke Therapeutics, Inc.</issuerName><issuerTradingSymbol>STOK</issuerTradingSymbol></issuer>
<reportingOwner><reportingOwnerId><rptOwnerCik>0001</rptOwnerCik><rptOwnerName>{owner}</rptOwnerName></reportingOwnerId><reportingOwnerRelationship>{rel}</reportingOwnerRelationship></reportingOwner>
<nonDerivativeTable>{nd}</nonDerivativeTable><derivativeTable>{dv}</derivativeTable></ownershipDocument>"""

# tiny 1x1 jpeg-ish placeholder (a real small GIF)
gif = bytes.fromhex("47494638396101000100800000000000ffffff21f90401000000002c00000000010001000002024401003b")
for r in recent:
    a = r["accessionNumber"]; nod = a.replace("-", "")
    base = f"{www}/Archives/edgar/data/{CIK}/{nod}"
    doc = r["primaryDocument"]
    rows = []
    if r["form"] == "8-K":
        ex = f"stok-ex99_1.htm"
        rows = [("1", "8-K", f"/ix?doc=/Archives/edgar/data/{CIK}/{nod}/{doc}", doc, "8-K", 24001),
                ("2", "EX-99.1", f"/Archives/edgar/data/{CIK}/{nod}/{ex}", ex, "EX-99.1", 61002),
                ("3", "", f"/Archives/edgar/data/{CIK}/{nod}/g1.gif", "g1.gif", "GRAPHIC", 43),
                ("&nbsp;", "Complete submission text file", f"/Archives/edgar/data/{CIK}/{nod}/{a}.txt", f"{a}.txt", "&nbsp;", 300000)]
        w(f"{base}/{ex}", doc_html("Stoke Therapeutics Reports Financial Results (press release)", "Press release", "g1.gif").replace("<body", "<body", 1).replace("</h3>", "</h3>" + GUIDANCE, 1))
        w(f"{base}/g1.gif", gif)
    elif r["form"] == "4":
        w(f"{base}/wk-form4_1.xml", form4_xml(r))
        rows = [("1", "FORM 4", f"/Archives/edgar/data/{CIK}/{nod}/{doc}", "wk-form4_1.html", "4", 5000),
                ("1", "FORM 4", f"/Archives/edgar/data/{CIK}/{nod}/wk-form4_1.xml", "wk-form4_1.xml", "4", 4000)]
    else:
        rows = [("1", r["form"], f"/ix?doc=/Archives/edgar/data/{CIK}/{nod}/{doc}", doc, r["form"], 2500001),
                ("2", "EX-10.1", f"/Archives/edgar/data/{CIK}/{nod}/stok-ex10_1.htm", "stok-ex10_1.htm", "EX-10.1", 90001),
                ("3", "EX-31.1", f"/Archives/edgar/data/{CIK}/{nod}/stok-ex31_1.htm", "stok-ex31_1.htm", "EX-31.1", 9001),
                ("4", "", f"/Archives/edgar/data/{CIK}/{nod}/logo.gif", "logo.gif", "GRAPHIC", 43)]
        w(f"{base}/stok-ex10_1.htm", doc_html("Exhibit 10.1 License Agreement", "Agreement"))
        w(f"{base}/stok-ex31_1.htm", doc_html("Exhibit 31.1 Certification", "Certification"))
        w(f"{base}/logo.gif", gif)
    w(f"{base}/{a}-index.htm", index_page(a, rows))
    w(f"{base}/{doc}", doc_html(f"FORM {r['form']} for period {r['reportDate']}", r["form"], "logo.gif" if r["form"] != "8-K" else "g1.gif"))
# old S-1 has no index page -> exercises index.json fallback
o = old[0]; nod = o["accessionNumber"].replace("-", "")
base = f"{www}/Archives/edgar/data/{CIK}/{nod}"
w(f"{base}/{o['primaryDocument']}", doc_html("FORM S-1 Registration Statement", "S-1"))
w(f"{base}/d123dex991.htm", doc_html("Exhibit 99.1 Consent", "Consent"))
w(f"{base}/index.json", json.dumps({"directory": {"item": [
    {"name": o["primaryDocument"], "type": "file", "size": "4000000"},
    {"name": "d123dex991.htm", "type": "file", "size": "1000"},
    {"name": "R1.htm", "type": "file", "size": "10"},
    {"name": o["accessionNumber"] + ".txt", "type": "file", "size": "10"}], "name": "x"}}))

# ---------------------------------------------------------------- company facts
facts = {}
acc_by_filed = {r["filingDate"]: r["accessionNumber"] for r in recent if r["form"] in ("10-Q", "10-K")}
def add(tax, concept, unit, f):
    if f.get("accn") in (None, "x"):
        f["accn"] = acc_by_filed.get(f["filed"], "0000000000-00-000001")
    facts.setdefault(tax, {}).setdefault(concept, {"label": concept, "units": {}})["units"].setdefault(unit, []).append(f)

def qdates(y):
    return [(f"{y}-01-01", f"{y}-03-31"), (f"{y}-04-01", f"{y}-06-30"), (f"{y}-07-01", f"{y}-09-30"), (f"{y}-10-01", f"{y}-12-31")]

rd_q = {}; ga_q = {}; rev_q = {}; ocf_q = {}
for y in [2022, 2023, 2024, 2025]:
    for i in range(4):
        k = (y, i)
        rd_q[k] = 20e6 + (y - 2022) * 3e6 + i * 0.5e6
        ga_q[k] = 9e6 + (y - 2022) * 1e6
        rev_q[k] = 0 if y < 2024 else 4e6 + i * 1e6
        ocf_q[k] = -(rd_q[k] + ga_q[k] - rev_q[k]) * 0.85
filed_for = {0: "-05-08", 1: "-08-07", 2: "-11-06"}
for y in [2022, 2023, 2024, 2025]:
    qs = qdates(y)
    for i in range(4):
        if y == 2025 and i == 3:
            break
        start, end = qs[i]
        if i < 3:
            filed = f"{y}{filed_for[i]}"; form = "10-Q"
            for concept, d in [("ResearchAndDevelopmentExpense", rd_q), ("GeneralAndAdministrativeExpense", ga_q),
                               ("RevenueFromContractWithCustomerExcludingAssessedTax", rev_q)]:
                v = d[(y, i)]
                add("us-gaap", concept, "USD", dict(start=start, end=end, val=v, accn="x", fy=y, fp=f"Q{i+1}", form=form, filed=filed, frame=f"CY{y}Q{i+1}"))
                tot = sum(d[(y, j)] for j in range(i + 1))
                if i > 0:
                    add("us-gaap", concept, "USD", dict(start=f"{y}-01-01", end=end, val=tot, accn="x", fy=y, fp=f"Q{i+1}", form=form, filed=filed))
            opex = rd_q[(y, i)] + ga_q[(y, i)]
            add("us-gaap", "OperatingExpenses", "USD", dict(start=start, end=end, val=opex, form=form, filed=filed))
            add("us-gaap", "NetIncomeLoss", "USD", dict(start=start, end=end, val=rev_q[(y, i)] - opex + 2e6, form=form, filed=filed))
            add("us-gaap", "EarningsPerShareBasicAndDiluted", "USD/shares", dict(start=start, end=end, val=round((rev_q[(y, i)] - opex + 2e6) / 50e6, 2), form=form, filed=filed))
            add("us-gaap", "WeightedAverageNumberOfSharesOutstandingBasic", "shares", dict(start=start, end=end, val=45e6 + y - 2022 + i * 1e5, form=form, filed=filed))
            # cash flow only YTD
            add("us-gaap", "NetCashProvidedByUsedInOperatingActivities", "USD", dict(start=f"{y}-01-01", end=end, val=sum(ocf_q[(y, j)] for j in range(i + 1)), form=form, filed=filed))
            add("dei", "EntityCommonStockSharesOutstanding", "shares", dict(end=f"{y}{filed_for[i][:-2]}{'01'}", val=50e6 + (y - 2022) * 2e6 + i * 1e5, form=form, filed=filed))
        # instants at each quarter end
        filed = f"{y}{filed_for[i]}" if i < 3 else f"{y+1}-03-04"
        form = "10-Q" if i < 3 else "10-K"
        cash = 80e6 - (y - 2022) * 5e6 - i * 2e6
        add("us-gaap", "CashAndCashEquivalentsAtCarryingValue", "USD", dict(end=end, val=cash, form=form, filed=filed))
        add("us-gaap", "MarketableSecuritiesCurrent", "USD", dict(end=end, val=150e6 - (y - 2022) * 20e6 - i * 4e6, form=form, filed=filed))
        add("us-gaap", "Assets", "USD", dict(end=end, val=300e6 - (y - 2022) * 25e6, form=form, filed=filed))
        add("us-gaap", "Liabilities", "USD", dict(end=end, val=60e6, form=form, filed=filed))
        add("us-gaap", "StockholdersEquity", "USD", dict(end=end, val=240e6 - (y - 2022) * 25e6, form=form, filed=filed))
    if y < 2025:  # 10-K annual facts (no Q4 3-month facts)
        filed = f"{y+1}-03-04"
        for concept, d in [("ResearchAndDevelopmentExpense", rd_q), ("GeneralAndAdministrativeExpense", ga_q),
                           ("RevenueFromContractWithCustomerExcludingAssessedTax", rev_q), ("NetCashProvidedByUsedInOperatingActivities", ocf_q)]:
            add("us-gaap", concept, "USD", dict(start=f"{y}-01-01", end=f"{y}-12-31", val=sum(d[(y, j)] for j in range(4)), form="10-K", filed=filed, frame=f"CY{y}"))
        opex = sum(rd_q[(y, j)] + ga_q[(y, j)] for j in range(4))
        add("us-gaap", "OperatingExpenses", "USD", dict(start=f"{y}-01-01", end=f"{y}-12-31", val=opex, form="10-K", filed=filed))
        add("us-gaap", "NetIncomeLoss", "USD", dict(start=f"{y}-01-01", end=f"{y}-12-31", val=sum(rev_q[(y, j)] for j in range(4)) - opex + 8e6, form="10-K", filed=filed))
        add("dei", "EntityCommonStockSharesOutstanding", "shares", dict(end=f"{y+1}-02-25", val=50e6 + (y - 2022) * 2e6 + 4e5, form="10-K", filed=filed))
# a restated comparative (filed later) should win over the original
add("us-gaap", "ResearchAndDevelopmentExpense", "USD", dict(start="2024-07-01", end="2024-09-30", val=27777777, form="10-Q", filed="2025-11-06"))
# ---------------------------------------------------------------- inline XBRL documents
def ixdoc(r):
    a = r["accessionNumber"]
    ctx = {}; rows = []; hidden = []
    def cid(start, end):
        k = (start, end)
        if k not in ctx: ctx[k] = f"c-{len(ctx)+1}"
        return ctx[k]
    for tax, concepts in facts.items():
        for concept, cj in concepts.items():
            for unit, fs in cj["units"].items():
                for f in fs:
                    if f["accn"] != a: continue
                    c = cid(f.get("start"), f["end"])
                    v = f["val"]
                    if unit == "USD":
                        scale, disp = 3, f"{abs(v)/1000:,.0f}"
                    elif unit == "USD/shares":
                        scale, disp = 0, f"{abs(v):.2f}"
                    else:
                        scale, disp = 0, f"{abs(v):,.0f}"
                    sign = ' sign="-"' if v < 0 else ""
                    tag = f'<ix:nonFraction name="{tax}:{concept}" contextRef="{c}" unitRef="u1" decimals="-3" scale="{scale}" format="ixt:num-dot-decimal"{sign}>{disp}</ix:nonFraction>'
                    period = (f.get("start") + " to " if f.get("start") else "as of ") + f["end"]
                    if tax == "dei":
                        hidden.append(tag)
                    else:
                        rows.append(f'<tr><td>{concept}</td><td>{period}</td><td style="text-align:right">$ {"(" if v < 0 else ""}{tag}{")" if v < 0 else ""}</td></tr>')
    # a dimensional (segment) fact for R&D that must be ignored
    rd = [f for f in facts["us-gaap"]["ResearchAndDevelopmentExpense"]["units"]["USD"] if f["accn"] == a]
    dimctx = ""
    if rd:
        f = rd[0]
        dimctx = f'<xbrli:context id="c-dim"><xbrli:entity><xbrli:identifier scheme="http://www.sec.gov/CIK">{CIK:010d}</xbrli:identifier><xbrli:segment><xbrldi:explicitMember dimension="srt:ProductOrServiceAxis">stok:ProgramAMember</xbrldi:explicitMember></xbrli:segment></xbrli:entity><xbrli:period><xbrli:startDate>{f["start"]}</xbrli:startDate><xbrli:endDate>{f["end"]}</xbrli:endDate></xbrli:period></xbrli:context>'
        rows.insert(0, f'<tr><td>R&amp;D for one program (segment)</td><td></td><td>$ <ix:nonFraction name="us-gaap:ResearchAndDevelopmentExpense" contextRef="c-dim" unitRef="u1" decimals="-3" scale="3">1,234</ix:nonFraction></td></tr>')
    ctxxml = "".join(
        f'<xbrli:context id="{i}"><xbrli:entity><xbrli:identifier scheme="http://www.sec.gov/CIK">{CIK:010d}</xbrli:identifier></xbrli:entity><xbrli:period>'
        + (f"<xbrli:startDate>{st}</xbrli:startDate><xbrli:endDate>{en}</xbrli:endDate>" if st else f"<xbrli:instant>{en}</xbrli:instant>")
        + "</xbrli:period></xbrli:context>" for (st, en), i in ctx.items())
    paras = "".join(f"<p>Notes paragraph {i}. Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore.</p>" for i in range(60))
    return f"""<html xmlns="http://www.w3.org/1999/xhtml" xmlns:ix="http://www.xbrl.org/2013/inlineXBRL" xmlns:xbrli="http://www.xbrl.org/2003/instance"><head><title>FORM {r['form']} for period {r['reportDate']}</title></head>
<body style="font-family:Times New Roman"><div style="display:none"><ix:header><ix:hidden>{''.join(hidden)}</ix:hidden><ix:resources>{ctxxml}{dimctx}</ix:resources></ix:header></div>
<h2 style="text-align:center">UNITED STATES SECURITIES AND EXCHANGE COMMISSION</h2><h3 style="text-align:center">FORM {r['form']}</h3>{paras}{GUIDANCE}
<h3>CONDENSED STATEMENTS OF OPERATIONS (in thousands)</h3><table border="1" style="border-collapse:collapse">{''.join(rows)}</table>{paras}</body></html>"""

for r in recent:
    if r["form"] in ("10-Q", "10-K"):
        nod = r["accessionNumber"].replace("-", "")
        w(f"{www}/Archives/edgar/data/{CIK}/{nod}/{r['primaryDocument']}", ixdoc(r))

w(f"{data}/api/xbrl/companyfacts/CIK{CIK:010d}.json", json.dumps({"cik": CIK, "entityName": "Stoke Therapeutics, Inc.", "facts": facts}))
print("mock written to", root)
