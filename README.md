# SECquence

A Windows 10/11 desktop tool for researching public companies from their SEC filings.
It replaces SEC Filings Viewer; on first run it brings over your email, download
folder and watchlist.

## Getting it
Download **SECquence.exe** from the latest release on this repository's Releases
page and put it in a folder you can write to, such as Documents.

## Running it
Double-click **SECquence.exe**. There is nothing to install: it uses the Microsoft
Edge that ships with Windows. The first time, Windows SmartScreen may say "Windows
protected your PC" because the app isn't code-signed. Choose **More info**, then
**Run anyway**.

## What's in it
| Tab | What it does |
|---|---|
| Overview | Price, cash and runway, next catalysts, latest filings and news, your valuation, insider activity. |
| Filings | Every filing on EDGAR with filters and a preview pane. Save one document or many filings as PDFs. Arrow keys move through the list. |
| Financials | Quarterly and annual figures from XBRL. Select any number to see its source filing with the number highlighted. Export to Excel with a Sources sheet. |
| Stock | Price chart (1D to All), day and 52-week ranges, market cap, enterprise value, cash per share. |
| News | Google News and Yahoo Finance articles merged with the company's 8-K filings. |
| Catalysts | Company guidance quoted from recent filings, ClinicalTrials.gov completion dates, estimated earnings and meeting dates. |
| Valuation | Risk-adjusted NPV, sum-of-the-parts model following the Biotech sum-of-the-parts worksheet. Saves per company; exports an Excel workbook with live formulas. |
| Ownership | Insider trades from Form 4 filings; share count history, money raised and offering documents. |

**Compare watchlist** (bottom left) puts watched companies side by side: price,
market cap, cash, enterprise value, burn and runway.

The strip under the company name shows the last six years of filings, one lane per
type. Select a band to open that filing.

## Updates
At startup SECquence looks at this repository's latest release. If it is newer, an
**Update** button appears at the bottom left; it downloads the new version, checks
it against the release's checksum, replaces the program file and restarts.
Settings has a **Check for updates** link. Nothing is installed without asking.

## Sessions
A session is your watchlist and saved valuation models. **Session** (under the
search box) saves it to a `.secq` file, opens one, or starts an empty one, so work
can move between computers or be kept apart for each person.

## Model versions and model files
The Valuation tab keeps several named versions of a company's model, such as
"Base case" and "Bull case". **Save as** makes a new one; the Version menu
switches between them. **More** saves one model to its own `.secqmodel` file or
opens such a file, to pass a single model to someone else.

## Website Resources
Lists the files a company posts on its own website and does not file with the
SEC: investor slide decks, conference posters, scientific presentations and
publications. It reads the home page and the pages that look like investor
events, science and publications. Sites differ, so it is a best effort; add a
page by hand under "Website and pages" when something is missing.

## Where things are kept
- PDFs and Excel exports: `Documents\SEC Filings\<TICKER>\` (change in Settings).
- Settings, watchlist and saved valuation models: `%APPDATA%\SECquence\`
- Cache and log: `%LOCALAPPDATA%\SECquence\` (`app.log` helps with troubleshooting).

## Data sources
SEC EDGAR (filings, XBRL financials, Form 4s), ClinicalTrials.gov, Yahoo Finance
prices with Stooq daily closes as a fallback, Google News and Yahoo Finance news
feeds. Prices and news come from unofficial public endpoints and can lag or change.

## Self-test
In Command Prompt, from the folder holding the exe:
```
SECquence.exe --selftest STOK --email you@example.com > selftest.txt
```
It checks every data source against live services, including PDF conversion.

## Source
Go 1.24, standard library only. `go test ./...` runs the tests against mock services
built by the scripts in `testdata/`. To build:
```
set GOOS=windows& set GOARCH=amd64& go build -trimpath -ldflags "-H windowsgui -s -w" -o SECquence.exe .
```

## Building
Go 1.24 or later, no other dependencies:

    go test ./...
    GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o SECquence.exe .

Releases are built by GitHub Actions when `AppVersion` in `main.go` changes on `main`
(`.github/workflows/release.yml`), which also adds the icon and version details.

This is a personal research tool. Nothing it shows is investment advice.
