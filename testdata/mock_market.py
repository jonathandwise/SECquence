"""Mock Yahoo Finance chart API, Stooq CSV and Google/Yahoo news RSS for offline UI tests.
Synthetic data only. Usage: python3 mock_market.py PORT"""
import json, math, random, sys, datetime as dt
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs
from zoneinfo import ZoneInfo

NY = ZoneInfo("America/New_York")
FAIL_YAHOO = "--fail-yahoo" in sys.argv

def last_session():
    d = dt.datetime.now(NY).date()
    while d.weekday() >= 5:
        d -= dt.timedelta(days=1)
    return d

def sessions(n, end):
    out, d = [], end
    while len(out) < n:
        if d.weekday() < 5:
            out.append(d)
        d -= dt.timedelta(days=1)
    return out[::-1]

def walk(n, start, seed, vol=0.02):
    r = random.Random(seed); p = start; out = []
    for i in range(n):
        p *= math.exp(r.gauss(0.0004, vol)); out.append(round(p, 2))
    return out

def series(rng):
    end = last_session()
    pts = []
    if rng == "1d":
        day = end
        ts = [dt.datetime.combine(day, dt.time(9, 30), NY) + dt.timedelta(minutes=5 * i) for i in range(78)]
        closes = walk(78, 12.1, 1, 0.003)
    elif rng == "5d":
        ts = [dt.datetime.combine(d, dt.time(9, 30), NY) + dt.timedelta(minutes=15 * i) for d in sessions(5, end) for i in range(26)]
        closes = walk(len(ts), 11.5, 2, 0.006)
    elif rng == "1mo":
        ts = [dt.datetime.combine(d, dt.time(9, 30), NY) + dt.timedelta(hours=i) for d in sessions(21, end) for i in range(7)]
        closes = walk(len(ts), 10.8, 3, 0.01)
    else:
        if rng in ("5y", "max"):
            first = dt.date(2019, 6, 21) if rng == "max" else end - dt.timedelta(days=5 * 365)
            days = []; d = first
            while d <= end:
                days.append(d); d += dt.timedelta(days=7)
        else:
            n = {"6mo": 126, "1y": 252}.get(rng)
            if rng == "ytd":
                n = len([d for d in sessions(260, end) if d.year == end.year])
            days = sessions(n, end)
        ts = [dt.datetime.combine(d, dt.time(9, 30), NY) for d in days]
        closes = walk(len(ts), 18.0 if rng == "max" else 9.0, 4, 0.03)
    # scale so that every range ends at the same "current" price
    k = 12.34 / closes[-1]
    closes = [round(c * k, 2) for c in closes]
    vols = [int(200000 + 150000 * abs(math.sin(i))) for i in range(len(ts))]
    closes[len(closes) // 3] = None  # a gap, like real data
    return [int(t.timestamp()) for t in ts], closes, vols

def chart(sym, rng, interval):
    t, c, v = series(rng)
    meta = {"currency": "USD", "symbol": sym, "exchangeName": "NMS", "fullExchangeName": "NasdaqGS", "instrumentType": "EQUITY",
            "exchangeTimezoneName": "America/New_York", "timezone": "EST", "regularMarketPrice": 12.34,
            "fiftyTwoWeekHigh": 17.58, "fiftyTwoWeekLow": 5.35, "regularMarketDayHigh": 12.62, "regularMarketDayLow": 11.95,
            "regularMarketVolume": 731245, "longName": "Stoke Therapeutics, Inc.", "shortName": "Stoke Therapeutics, Inc.",
            "chartPreviousClose": 12.02, "regularMarketTime": t[-1], "dataGranularity": interval, "range": rng}
    if rng == "1d":
        meta["previousClose"] = 12.02
    return {"chart": {"result": [{"meta": meta, "timestamp": t, "indicators": {"quote": [{"close": c, "volume": v, "open": c, "high": c, "low": c}]}}], "error": None}}

def rss(items, title):
    out = [f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>{title}</title>']
    for it in items:
        src = f'<source url="{it["srcurl"]}">{it["src"]}</source>' if it.get("src") and it.get("srcurl") else ""
        out.append(f'<item><title>{it["title"]}</title><link>{it["link"]}</link><guid isPermaLink="false">{it["link"]}</guid><pubDate>{it["date"]}</pubDate><description>{it["desc"]}</description>{src}</item>')
    out.append("</channel></rss>")
    return "".join(out)

def rfc(days_ago, hour=13):
    d = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days_ago, hours=hour % 5)
    return d.strftime("%a, %d %b %Y %H:%M:%S GMT")

GNEWS = [
    dict(title="Stoke Therapeutics Reports Third Quarter 2026 Financial Results and Provides Business Updates - GlobeNewswire", src="GlobeNewswire", srcurl="https://www.globenewswire.com", link="https://news.google.com/rss/articles/CBMiAAA1?oc=5", date=rfc(6), desc="&lt;a href=&quot;https://news.google.com/rss/articles/CBMiAAA1?oc=5&quot;&gt;Stoke Therapeutics Reports Third Quarter 2026 Financial Results and Provides Business Updates&lt;/a&gt;&amp;nbsp;&amp;nbsp;&lt;font color=&quot;#6f6f6f&quot;&gt;GlobeNewswire&lt;/font&gt;"),
    dict(title="Stoke's zorevunersen Phase 3 EMPEROR study completes enrollment - Fierce Biotech", src="Fierce Biotech", srcurl="https://www.fiercebiotech.com", link="https://news.google.com/rss/articles/CBMiAAA2?oc=5", date=rfc(2), desc="Fierce Biotech"),
    dict(title="Analysts raise price targets on Stoke Therapeutics (STOK) after data - Investing.com", src="Investing.com", srcurl="https://www.investing.com", link="https://news.google.com/rss/articles/CBMiAAA3?oc=5", date=rfc(1), desc="Investing.com"),
    dict(title="Dravet syndrome treatment landscape: what antisense oligonucleotides could change - BioSpace", src="BioSpace", srcurl="https://www.biospace.com", link="https://news.google.com/rss/articles/CBMiAAA4?oc=5", date=rfc(12), desc="BioSpace"),
    dict(title="Stoke Therapeutics to Present at Upcoming Investor Conferences - GlobeNewswire", src="GlobeNewswire", srcurl="https://www.globenewswire.com", link="https://news.google.com/rss/articles/CBMiAAA5?oc=5", date=rfc(20), desc="GlobeNewswire"),
]
YNEWS = [
    dict(title="Stoke Therapeutics Reports Third Quarter 2026 Financial Results and Provides Business Updates", link="https://finance.yahoo.com/news/stoke-therapeutics-reports-third-quarter-q3.html", date=rfc(6), desc="Stoke Therapeutics, Inc. (Nasdaq: STOK), a biotechnology company dedicated to restoring protein expression by harnessing the body's potential with RNA medicine, today reported financial results for the third quarter."),
    dict(title="Is Stoke Therapeutics (STOK) stock a buy after its recent run?", link="https://finance.yahoo.com/news/stok-buy-after-run.html", date=rfc(3), desc="Shares have climbed sharply over the past month as investors look ahead to Phase 3 data in Dravet syndrome."),
    dict(title="Biotech stocks moving after hours: STOK, SRPT, IONS", link="https://finance.yahoo.com/news/biotech-movers.html", date=rfc(0, 3), desc="A roundup of notable after-hours moves in biotech."),
]

CTGOV = {"studies": [
  {"protocolSection": {"identificationModule": {"nctId": "NCT06872125", "briefTitle": "A Study of Zorevunersen in Children and Adolescents With Dravet Syndrome", "acronym": "EMPEROR"},
   "statusModule": {"overallStatus": "RECRUITING", "startDateStruct": {"date": "2025-06", "type": "ACTUAL"}, "primaryCompletionDateStruct": {"date": "2027-09", "type": "ESTIMATED"},
                    "completionDateStruct": {"date": "2028-03-31", "type": "ESTIMATED"}, "lastUpdatePostDateStruct": {"date": "2026-10-01"}},
   "sponsorCollaboratorsModule": {"leadSponsor": {"name": "Stoke Therapeutics", "class": "INDUSTRY"}},
   "conditionsModule": {"conditions": ["Dravet Syndrome"]},
   "designModule": {"phases": ["PHASE3"], "enrollmentInfo": {"count": 150, "type": "ESTIMATED"}},
   "armsInterventionsModule": {"interventions": [{"type": "DRUG", "name": "Zorevunersen"}, {"type": "OTHER", "name": "Sham Comparator"}]}}},
  {"protocolSection": {"identificationModule": {"nctId": "NCT04740476", "briefTitle": "Open-Label Extension Study of STK-001 in Patients With Dravet Syndrome", "acronym": "SWALLOWTAIL"},
   "statusModule": {"overallStatus": "ACTIVE_NOT_RECRUITING", "startDateStruct": {"date": "2021-06-01", "type": "ACTUAL"}, "primaryCompletionDateStruct": {"date": "2027-02-28", "type": "ESTIMATED"},
                    "completionDateStruct": {"date": "2027-06-30", "type": "ESTIMATED"}, "lastUpdatePostDateStruct": {"date": "2026-08-12"}},
   "sponsorCollaboratorsModule": {"leadSponsor": {"name": "Stoke Therapeutics", "class": "INDUSTRY"}},
   "conditionsModule": {"conditions": ["Dravet Syndrome"]},
   "designModule": {"phases": ["PHASE1", "PHASE2"], "enrollmentInfo": {"count": 74, "type": "ACTUAL"}},
   "armsInterventionsModule": {"interventions": [{"type": "DRUG", "name": "STK-001"}]}}},
  {"protocolSection": {"identificationModule": {"nctId": "NCT04442295", "briefTitle": "Study of STK-001 in Children and Adolescents With Dravet Syndrome", "acronym": "MONARCH"},
   "statusModule": {"overallStatus": "COMPLETED", "startDateStruct": {"date": "2020-08-04", "type": "ACTUAL"}, "primaryCompletionDateStruct": {"date": "2024-03-28", "type": "ACTUAL"},
                    "completionDateStruct": {"date": "2024-03-28", "type": "ACTUAL"}, "lastUpdatePostDateStruct": {"date": "2025-01-10"}},
   "sponsorCollaboratorsModule": {"leadSponsor": {"name": "Stoke Therapeutics", "class": "INDUSTRY"}},
   "conditionsModule": {"conditions": ["Dravet Syndrome"]},
   "designModule": {"phases": ["PHASE1", "PHASE2"], "enrollmentInfo": {"count": 47, "type": "ACTUAL"}},
   "armsInterventionsModule": {"interventions": [{"type": "DRUG", "name": "STK-001"}]}}},
  {"protocolSection": {"identificationModule": {"nctId": "NCT09999999", "briefTitle": "Unrelated study"},
   "statusModule": {"overallStatus": "RECRUITING", "primaryCompletionDateStruct": {"date": "2027-01", "type": "ESTIMATED"}},
   "sponsorCollaboratorsModule": {"leadSponsor": {"name": "Stokes Regional Hospital"}}}}
]}

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def send(self, code, body, ctype):
        b = body.encode() if isinstance(body, str) else body
        self.send_response(code); self.send_header("Content-Type", ctype); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        u = urlparse(self.path); q = parse_qs(u.query)
        if u.path.startswith("/v8/finance/chart/"):
            sym = u.path.rsplit("/", 1)[-1]
            if FAIL_YAHOO:
                return self.send(429, "Too Many Requests", "text/plain")
            if sym != "STOK":
                return self.send(404, json.dumps({"chart": {"result": None, "error": {"code": "Not Found", "description": "No data found, symbol may be delisted"}}}), "application/json")
            return self.send(200, json.dumps(chart(sym, q.get("range", ["1d"])[0], q.get("interval", ["5m"])[0])), "application/json")
        if u.path == "/q/d/l/":
            t, c, v = series("max")
            rows = ["Date,Open,High,Low,Close,Volume"]
            for ts, cl, vo in zip(t, c, v):
                if cl is None: continue
                rows.append(f"{dt.datetime.fromtimestamp(ts, NY).date()},{cl},{cl},{cl},{cl},{vo}")
            return self.send(200, "\n".join(rows), "text/csv")
        if u.path == "/api/v2/studies":
            return self.send(200, json.dumps(CTGOV), "application/json")
        if u.path == "/rss/search":
            return self.send(200, rss(GNEWS, "Google News"), "application/rss+xml")
        if u.path == "/rss/2.0/headline":
            return self.send(200, rss(YNEWS, "Yahoo! Finance: STOK News"), "application/rss+xml")
        self.send(404, "not found", "text/plain")

HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
