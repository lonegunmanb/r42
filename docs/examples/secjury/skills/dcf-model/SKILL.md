---
name: dcf-model
description: Build an auditable company DCF from public evidence and return a machine-readable JSON model with historicals, projections, WACC, terminal value, equity bridge, sensitivities, and source provenance.
---

# DCF Model Builder

Build an unlevered free-cash-flow DCF for an operating company. The deliverable is JSON, never an Excel workbook. A consumer must be able to validate, diff, and re-run the model without parsing prose.

## Output Contract

Return one JSON object with exactly two top-level fields: `model` and `sources`. Do not wrap it in Markdown and do not create `.xlsx` files. The consumer writes these fields to separate artifacts.

Required output structure:

```json
{
  "model": {
    "schema_version": "dcf-model.v2",
    "company": {"name": "", "ticker": "", "exchange": "", "currency": ""},
    "valuation_date": "YYYY-MM-DD",
    "assumptions": {"wacc": 0.0, "terminal_growth": 0.0},
    "historical": [],
    "projections": [],
    "valuation": {},
    "sensitivity": []
  },
  "sources": []
}
```

The consumer saves `model` as `modeling/dcf-model.json` and `sources` as `modeling/dcf-sources.json`. Do not include a `sources` field inside `model`.

Use decimal rates (`0.10`, not `10`). Monetary values must use the company's stated reporting currency and one consistent scale. Shares must use the same scale as equity value so per-share value is dimensionally correct. Use JSON numbers, not formatted currency strings.

Each historical and projection item contains:

`period`, `revenue`, `revenue_growth`, `ebit`, `ebit_margin`, `tax_rate`, `nopat`, `da`, `capex`, `change_nwc`, `ufcf`, `discount_period`, `discount_factor`, `pv_ufcf`.

Historical items may use zero for fields that do not apply. Projection items must populate every field. Include 3-5 historical years and 5-10 forecast years.

The `valuation` object contains:

`pv_explicit_fcf`, `terminal_fcf`, `terminal_value`, `pv_terminal_value`, `enterprise_value`, `net_debt`, `equity_value`, `diluted_shares`, `implied_value_per_share`, `current_price`, `implied_return`.

Each sensitivity item contains `wacc`, `terminal_growth`, and `implied_value_per_share`. Each item in the top-level `sources` array contains a stable `id`, `title`, absolute `url`, `published_date` when known, and `accessed_date`.

## Evidence Rules

Use information supplied by the user first, then primary public filings and investor-relations materials, then reputable market-data sources. Prefer audited annual filings over aggregators. Cross-check current price, diluted shares, cash, and debt against a second source when practical.

Never silently invent a value. When an input cannot be observed:

- derive it from cited inputs when the derivation is standard;
- otherwise use a conservative estimate only when estimates are allowed;
- disclose the estimate and rationale in the source title or a clearly identified source record;
- do not manufacture a URL, filing date, or precision.

All sources must have been public on or before `valuation_date`. Do not use later information with hindsight. Distinguish fiscal period end, filing publication date, and market-price date.

## Workflow

### 1. Identify the security

Confirm legal company name, ticker, exchange, reporting currency, fiscal year end, and the listed security whose diluted share count and price are used. Stop if the target is ambiguous.

### 2. Normalize historical data

Collect 3-5 fiscal years of revenue, EBIT, D&A, capital expenditure, change in net working capital, cash, debt, and diluted shares. Reconcile sign conventions:

- CapEx is a positive cash outflow in the JSON.
- `change_nwc` is positive when working capital consumes cash.
- Net debt is total debt minus cash and cash equivalents; net cash is negative net debt.
- Exclude financing cash flows from UFCF.

Check revenue CAGR, EBIT margin, cash conversion, capital intensity, working-capital behavior, and one-off items before forecasting.

### 3. Forecast operations

Forecast revenue and EBIT using explicit, economically justified drivers. Growth should normally fade toward a sustainable long-run rate. Margins must reflect competition, capacity, operating leverage, and reinvestment needs rather than mechanically extrapolating the best historical year.

For every forecast year:

```text
Revenue_t = Revenue_(t-1) * (1 + revenue_growth_t)
EBIT_t = Revenue_t * ebit_margin_t
NOPAT_t = EBIT_t * (1 - tax_rate_t)
UFCF_t = NOPAT_t + D&A_t - CapEx_t - change_nwc_t
```

Do not treat D&A and CapEx as interchangeable. For a growing capital-intensive business, explain why CapEx is sufficient to support the revenue path.

### 4. Calculate WACC

Use market-value weights:

```text
Cost of equity = risk-free rate + beta * equity risk premium
After-tax cost of debt = pre-tax cost of debt * (1 - marginal tax rate)
WACC = equity_weight * cost_of_equity + debt_weight * after-tax_cost_of_debt
```

Use a risk-free rate, equity risk premium, beta, borrowing cost, and capital structure appropriate to the company's market and valuation date. Treat a net-cash company carefully; do not create a nonsensical negative debt weight. WACC must be positive and greater than terminal growth.

### 5. Discount explicit cash flows

Use the mid-year convention unless facts justify year-end discounting:

```text
discount_period_t = 0.5, 1.5, 2.5, ...
discount_factor_t = 1 / (1 + WACC) ^ discount_period_t
pv_ufcf_t = UFCF_t * discount_factor_t
pv_explicit_fcf = sum(pv_ufcf_t)
```

Keep the convention consistent between explicit cash flow and terminal value.

### 6. Calculate terminal value

Use perpetuity growth as the primary method:

```text
terminal_fcf = final_year_ufcf * (1 + terminal_growth)
terminal_value = terminal_fcf / (WACC - terminal_growth)
pv_terminal_value = terminal_value * final_year_discount_factor
```

Terminal growth must be below WACC and defensible relative to long-run nominal economic growth in the relevant currency. Flag a model whose terminal value dominates enterprise value; do not hide that dependency.

### 7. Bridge to equity value

```text
enterprise_value = pv_explicit_fcf + pv_terminal_value
equity_value = enterprise_value - net_debt
implied_value_per_share = equity_value / diluted_shares
implied_return = implied_value_per_share / current_price - 1
```

Add minority interest, pensions, leases, associates, or non-operating assets to net debt only when material and consistently defined. The JSON's `net_debt` must contain the aggregate adjustment actually used by the bridge.

### 8. Build sensitivity analysis

Produce an odd square grid, normally 5x5 or 7x7, for WACC versus terminal growth. Recalculate the full DCF at every point. The exact base WACC/base terminal-growth pair must be present at the center and its per-share value must equal `valuation.implied_value_per_share` within rounding tolerance.

Reject any grid point where terminal growth is greater than or equal to WACC rather than emitting infinity or a misleading value.

## Validation Checklist

Before returning JSON, verify:

- all required fields are present and all numeric values are finite;
- projected revenue, EBIT, NOPAT, UFCF, discount factors, and PVs reconcile;
- WACC is greater than terminal growth;
- enterprise value equals explicit-period PV plus terminal-value PV;
- equity value equals enterprise value minus net debt;
- per-share value equals equity value divided by diluted shares;
- implied return reconciles to current price;
- sensitivity contains an exact base-case center;
- sources are real, dated, and no later than the valuation date;
- the model does not claim precision unsupported by its inputs.

## Jury Handoff

The model is frozen once emitted. Jurors may challenge evidence, assumptions, formulas, internal consistency, reinvestment, terminal dependence, sensitivities, and margin of safety. They must refer to exact JSON paths, must not edit the model, and must distinguish model output from their judgment.
