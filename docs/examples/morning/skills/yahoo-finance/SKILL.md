---
name: yahoo-finance
description: Yahoo-first quote lookups for overnight prices, daily history, market state, and a small macro dashboard used by the financial-breakfast overnight-market researcher.
license: MIT
allowed-tools: Read Bash(yf:*) Bash(uv:*)
metadata:
  author: 0juano, adapted for r42 morning
  version: "1.1.0-morning"
---

# Yahoo Finance for the morning desk

Use this skill as the first source for an assigned quote scan. It provides quick
lookups for overnight equities, FX, volatility, commodities, and recent daily
history. If Yahoo has no usable result, the host workflow falls back to Jin10;
do not call Jin10 before attempting the Yahoo lookup.

The script is `{baseDir}/scripts/yf`. On Windows invoke it through `uv`:

```powershell
uv run --script {baseDir}\scripts\yf macro --json
uv run --script {baseDir}\scripts\yf price ^GSPC --json
uv run --script {baseDir}\scripts\yf quote CNH=X --json
uv run --script {baseDir}\scripts\yf history GC=F 5d --json
```

On macOS or Linux use the same `uv run --script` form with `/` separators.
Always request `--json`. Save the complete JSON output as a source artifact
before relying on it, and retain the symbol, observation time, trading date,
currency, exchange timezone, and market state.

## Supported commands

| Command | Use |
|---|---|
| `price TICKER` | Last daily observation and change from the preceding close |
| `quote TICKER` | Quote metadata plus observation time and market state |
| `history TICKER PERIOD` | Daily OHLCV history; periods include `5d`, `1mo`, `3mo`, `1y` |
| `macro` | Curated overnight dashboard with explicit Yahoo symbols |

The macro dashboard labels `^IRX` correctly as the 13-week US Treasury bill
yield and `^TNX` as the US 10-year yield. Values are reported in the units Yahoo
publishes; do not relabel differences as basis points without doing and showing
the conversion.

## Boundaries

- Yahoo does not provide a dependable FTSE China A50 futures series here. Do not
  substitute an ETF and call it A50 futures. Use Jin10 or another direct source.
- `CNH=X`, `000001.SS`, `^HSI`, and `^HXC` can be useful cross-checks, but verify
  freshness and market state before publication.
- This trimmed skill intentionally excludes news, analyst ratings, options,
  ETF "flows", fundamentals, and credit. Those commands had ambiguous freshness
  or semantics and are not needed for the overnight-market assignment.
- A missing value is `null`, not zero. Never infer a live quote from a previous
  close, and never describe delayed or closed-market data as real time.

For the bounded morning quote fallback, a usable Yahoo result is the selected
quote and no second source is needed. If Yahoo fails, preserve the failure and
the subsequent Jin10 result (or unavailable status) in separate artifacts. The
skill output is data, not evidence by itself; the retained artifact and its
timestamps are what the evidence workflow reviews.
