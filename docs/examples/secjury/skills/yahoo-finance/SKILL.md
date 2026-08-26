---
name: yahoo-finance
description: >
  Financial data from Yahoo Finance: stock/bond prices, credit analysis, macro dashboard,
  FX rates, ETF flows, fundamentals, and news. Use when the user asks about stock prices,
  bond yields, credit metrics, leverage ratios, debt/ebitda, interest coverage, macro
  indicators (VIX, DXY, treasury yields, oil, gold, BTC), LatAm FX (ARS, BRL, CLP, MXN, COP),
  ETF holdings, income statements, balance sheets, cash flow, financial news, options chains,
  dividend history, or analyst ratings/upgrades.
  Commands: yf price, yf quote, yf compare, yf credit, yf macro, yf fx, yf flows, yf history,
  yf fundamentals, yf news, yf search, yf options, yf dividends, yf ratings.
license: MIT
compatibility: Requires Python 3.10+ and uv
allowed-tools: Read Bash(yf:*) Bash(uv:*)
metadata:
  author: 0juano
  version: "1.0.1"
  openclaw:
    emoji: "📈"
    requires:
      bins: ["uv", "python3"]
    install:
      - id: uv
        kind: download
        url: https://astral.sh/uv/install.sh
        bins: ["uv"]
        label: "Install uv (Python package manager)"
---

# Yahoo Finance CLI

Financial data terminal powered by Yahoo Finance. All commands via the `yf` script.

## Setup

The script is at `{baseDir}/scripts/yf`. It uses `uv run --script` with inline PEP 723 metadata — dependencies (`yfinance`, `rich`) install automatically on first run. The only prerequisite is `uv`.

### macOS / Linux

```bash
chmod +x {baseDir}/scripts/yf
yf price AAPL
```

### Windows (PowerShell)

Windows does not honor the Unix shebang. Always invoke the script directly through `uv`:

```powershell
uv run --script {baseDir}\scripts\yf price AAPL
uv run --script {baseDir}\scripts\yf quote AAPL --json
```

Do not call a bare `yf` command and do not create or assume a PowerShell profile function.
Wherever this document shows `yf <command>`, Windows agents must expand it to
`uv run --script {baseDir}\scripts\yf <command>`.

## Commands

| Command | Purpose | Example |
|---------|---------|---------|
| `yf price TICKER` | Quick price + change + volume | `yf price YPF` |
| `yf quote TICKER` | Detailed quote (52w, PE, yield) | `yf quote AAPL` |
| `yf compare T1,T2,T3` | Side-by-side comparison table | `yf compare YPF,PAM,GGAL` |
| `yf credit TICKER` | Credit analysis: leverage, coverage, debt maturity | `yf credit YPF` |
| `yf macro` | Morning macro dashboard (UST, DXY, VIX, oil, gold, BTC, ARS) | `yf macro` |
| `yf fx [BASE]` | LatAm FX rates (ARS, BRL, CLP, MXN, COP) | `yf fx USD` |
| `yf flows ETF` | ETF top holdings + fund data | `yf flows EMB` |
| `yf history TICKER [PERIOD]` | Price history (1d/5d/1mo/3mo/6mo/1y/ytd/max) | `yf history YPF 3mo` |
| `yf fundamentals TICKER` | Full financials (IS, BS, CF) | `yf fundamentals YPF` |
| `yf news TICKER` | Recent news headlines | `yf news YPF` |
| `yf search QUERY` | Find tickers | `yf search "argentina bond"` |

All commands support `--json` for machine-readable output. JSON output follows RFC 8259:
missing or non-finite numeric values are emitted as `null`, never as `NaN` or `Infinity`.

## When to Use Which Command

- **Morning check**: `yf macro` → get UST yields, DXY, VIX, commodities, BTC, ARS in one shot
- **Quick look**: `yf price TICKER` → fast price/change/volume
- **Deep dive equity**: `yf quote` → `yf fundamentals` → `yf history`
- **Credit analysis**: `yf credit TICKER` → leverage ratios, interest coverage, debt breakdown
- **EM/LatAm FX**: `yf fx` → all major LatAm pairs vs USD
- **ETF research**: `yf flows ETF` → top holdings, AUM, expense ratio
- **Comparison**: `yf compare` → side-by-side for relative value

## Error Handling

The script handles bad tickers, missing data, and rate limits gracefully with clear error messages. If Yahoo Finance rate-limits, wait a moment and retry.

## Output

By default, output uses Rich tables for clean terminal display. Add `--json` to any command for structured JSON output suitable for piping or further processing. In JSON mode, stdout contains only the JSON document; diagnostics are written to stderr.
