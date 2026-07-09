---
name: xql
description: Run SQL over local CSV/TSV files and SharePoint Lists with the xql CLI. Use whenever a user asks to query, filter, aggregate, dedupe, profile, or bulk-edit tabular data in a CSV, TSV, SharePoint list, or xinglist — xql handles the SharePoint OData translation, preview-safe writes, and CSV type inference that raw sqlite3/DuckDB pipelines don't. Do NOT use for tasks that need JOINs, subqueries, CTEs, or SQL beyond a single-table subset — reach for DuckDB there instead.
---

# xql — Excelano Query Language CLI

`xql` binds to one tabular resource at startup and runs a small SQL grammar against it. Reads run immediately; writes preview a sample of affected rows and prompt for confirmation before committing. One grammar, three backends: local CSV/TSV (`csv`), Microsoft 365 SharePoint List (`sp`), and a hosted xinglist snapshot (`xinglet`, read-only).

Authoritative sources for this skill are the `xql` binary itself (`xql <backend> --help`, `describe` inside the REPL) and the maintainer-authored [GRAMMAR.md](https://github.com/excelano/xql/blob/main/GRAMMAR.md) and [README.md](https://github.com/excelano/xql/blob/main/README.md). This skill mirrors those; if it conflicts with them, they win.

## When to reach for xql (and when not to)

Reach for xql when the user has one CSV, one TSV, one SharePoint list, or one xinglist, and wants to filter, aggregate, profile, dedupe, or bulk-edit it with SQL. That is the whole story. It replaces spreadsheet-in-Excel, hand-written per-transform scripts, and — for SharePoint specifically — the PnP PowerShell "loop the list and PATCH each item" pattern.

Do not reach for xql if the task needs JOINs, subqueries, CTEs, `UNION`, window functions, or an expression on the right side of a comparison. Those are permanently out of scope by design — xql binds to one table per session. For anything that needs cross-table joins over CSV, use DuckDB. For anything that needs writes across multiple tables, use sqlite3 or the source system's own API.

## Version guard

The recipes and flags below assume xql 1.6.0 or newer (which adds `LOWER`/`UPPER`/`TRIM`, expression `GROUP BY`, and the `--describe` flag). Verify with `xql --version`. If the installed copy is older, either upgrade (`sudo apt install --only-upgrade xql` on Debian/Ubuntu, `brew upgrade xql` on macOS, or re-run the install one-liner from the README) or fall back to explicit rewrites (`SELECT column` instead of `SELECT LOWER(column)`).

## Dispatch — how xql picks a backend

The top-level dispatcher scans past any leading flags to find the first non-flag token, then routes on it:

1. If the token is a registered subcommand (`csv`, `sp`, `xinglet`, or `help`/`--help`/`-h`/`-V`/`--version`), that backend runs with the remaining args (leading flags preserved).
2. Otherwise, if the token has a recognized file extension (`.csv` or `.tsv`), the CSV backend runs with the full args.
3. Otherwise, error.

So all four of these are equivalent for a local CSV file:

```
xql csv data.csv
xql data.csv
xql --describe data.csv
xql data.csv --describe
```

SharePoint and xinglet are never inferred — they always require the explicit subcommand plus a URL:

```
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks
xql xinglet xinglet://4babff02-909f-4dba-b3df-3edf14b778bf
```

## SQL subset — what's in and what's out

Grammar shared across all three backends:

- `SELECT [DISTINCT] projection_list [WHERE ...] [GROUP BY expr, ...] [HAVING ...] [ORDER BY key, ...] [LIMIT n] [OFFSET m]`
- `UPDATE SET col = expr, ... [WHERE ...]`
- `DELETE [WHERE ...]`
- `INSERT (col, ...) VALUES (val, ...)`

Note the absence of `FROM`. The bound table is implicit — `xql csv data.csv` then `SELECT *` is enough.

Projections may include arithmetic (`price * qty AS line_total`), aggregates (`COUNT(*)`, `SUM`, `AVG`, `MIN`, `MAX`), and the three string-normalization scalars: `LOWER(s)`, `UPPER(s)`, `TRIM(s)`. Scalars can appear in both the projection list and `GROUP BY` (case-insensitive dedup uses this — see recipe 1). Unknown scalar names produce an "unknown function" error at plan time.

Predicates support `=`, `!=`, `<`, `>`, `<=`, `>=`, `IS [NOT] NULL`, `[NOT] LIKE`, `[NOT] ILIKE`, `[NOT] IN (...)`, `[NOT] BETWEEN a AND b`, and boolean composition with `AND`, `OR`, `NOT`. Left side is an expression (`WHERE price * qty > 100` is fine); right side is always a literal (`col1 = col2` is not supported).

Out of scope (permanent): `JOIN`, subqueries, `UNION`/`INTERSECT`/`EXCEPT`, CTEs, window functions. Not yet implemented: `ORDER BY` with expressions, `COUNT(DISTINCT col)`, further scalar functions (`LENGTH`, `SUBSTRING`, `YEAR`, etc.) — reserve those for a follow-up release.

Case-insensitive on keywords AND column names on input; output preserves the canonical header case. Two columns that differ only in case (`ID` and `id`) surface as an ambiguous-column error rather than a guess.

## Write safety

Every write (`INSERT`, `UPDATE`, `DELETE`) previews before it commits:

- **In the REPL:** xql prints the affected count, a sample of matching rows, then prompts `Apply? [y/N]:`. Anything but `y` cancels. Append `!` to the statement to skip the prompt and commit immediately (`UPDATE SET Status = 'Done' WHERE Priority > 3 !`). Bare `DELETE` (no `WHERE`) always prompts, even with `!`.
- **In `--exec` mode:** writes require `--commit`. Without it, they preview and exit. A bare `DELETE` additionally requires `--confirm-destructive` alongside `--commit`.
- **On xinglet:** all writes are rejected. The backend is read-only by design.

When a write commits, xql rewrites the bound file (CSV) or PATCHes each affected item (SharePoint). Use `--output PATH` to redirect committed writes to a different CSV instead of overwriting in place.

## Flags — the ones agents actually need

`xql csv <path>` accepts:

| Flag | Effect |
|------|--------|
| `--exec "<sql>"` | Run one statement and exit. |
| `--describe` | Print the schema and exit; no REPL, no SQL required. |
| `--commit` | Required to apply writes in `--exec` mode. |
| `--confirm-destructive` | Required to run a bare `DELETE` in `--exec` mode. |
| `--mode table\|tsv\|csv\|json` | Output format. Defaults to table on TTY, TSV when piped. |
| `--no-input-header` | Source has no header; columns become `col1`, `col2`, … |
| `--no-output-header` | Suppress the header row in output. |
| `--delim CHAR` | Field delimiter for non-comma files (use `\t` for tab). |
| `--type Col=int,Other=string` | Override the sampled type inference. |
| `--output PATH` | Write results (or committed table) to PATH as CSV. |

`xql sp <list-url>` adds `--all-fields` (include hidden/system columns in `SELECT *` and `--describe`). Same `--exec`, `--describe`, `--commit`, `--confirm-destructive`, `--mode`, `--no-output-header`, `--output` semantics as `csv`.

`xql xinglet xinglet://<uuid>` accepts `--exec`, `--describe`, `--mode`, `--no-output-header`, `--output`. `XINGLET_TOKEN` must be set in the environment. No write flags — the backend is read-only.

## REPL commands (plain words, no leading `\`)

Once inside the REPL, in addition to SQL statements:

- `help` or `?` — command help
- `describe` — column schema with inferred types; `describe all` on `sp` includes hidden columns
- `refresh` — re-read the source (useful after external edits)
- `mode <table|tsv|csv|json>` — set output format
- `headers on|off` — toggle the header row
- `output 'PATH'` — sticky redirect of SELECT results to PATH as CSV; bare `output` clears it
- `once 'PATH'` — redirect only the next statement
- `set all-fields on|off` (sp only) — include hidden columns in `SELECT *`; bare `set` lists current state
- `quit` or `exit` — leave

## CSV type inference — the gotchas that bite

xql samples the first 1024 rows of a CSV and infers `int`, `float`, `bool`, `date`, or `string` per column. The inferred type drives comparison behavior, so `Priority > 2` is a numeric compare, not a lexical one.

Three inference quirks worth knowing:

- **Leading-zero values stay `string`.** `"07030"`, `"007"`, `"-01"` are almost always identifiers, not numbers. Override with `--type Code=int` if the column really is numeric.
- **`NaN` and `Inf` demote a column to `string`.** Excel's `#DIV/0!` cells leak through Go's float parser, and NaN breaks SQL equality. If they appear anywhere in the sample, the column falls back to `string`.
- **Excel scientific notation stays `float`.** A column of `1.23E+12` will not round-trip back to the original integer text. Pin with `--type ID=string` to preserve literals.

Run `--describe` (or `describe` in the REPL) before writing anything to confirm what xql thinks each column is.

## SharePoint specifics

The SharePoint backend translates `WHERE` predicates to OData `$filter` and runs them server-side; `ORDER BY`, `LIMIT`, `OFFSET`, and `DISTINCT` apply client-side after the filtered fetch. This means huge lists filter fast but sort/paginate over the whole filtered set.

Predicate translation:

- `LIKE 'foo%'` → `startswith`
- `LIKE '%foo'` → `endswith`
- `LIKE '%foo%'` → `contains`
- Mid-pattern `%`, single-char `_` wildcards, and backslash escapes are rejected with a clear error — OData has no server-side equivalent.
- `ILIKE` wraps the column reference in `tolower()` and lowercases the literal.
- `IN (a, b, c)` → an OR'd equality chain.
- `BETWEEN a AND b` → `ge a and le b`.
- `IS NULL` / `IS NOT NULL` → `eq null` / `ne null`.

Column identity is dual: every column has both an **internal name** (what Graph uses, often `field_5`) and a **display name** (what the SharePoint UI shows). Either resolves. `describe` prints both when they differ. Two columns sharing a display name produce an ambiguous-column error listing the internal names.

Writes validate against the list's schema before any Graph round-trip. Person, Lookup, Hyperlink, and Calculated columns are all rejected on write with a clear message. Lookup fields on read return the numeric ID; write with that numeric ID, not the display text.

Auth is device-code OAuth. First run prints a short code + URL; a refresh token is cached at `~/.config/xql/sp-token.json` (mode 0600) for subsequent runs.

## Recipes

### 1. Case-insensitive dedup profile of a CSV

The canonical use for the scalar functions. Given an application inventory that has `CoStar`, `Costar`, and `costar` as three different values in `application_name`, collapse them and count the duplicates:

```sh
xql apps.csv --exec "SELECT LOWER(application_name) AS canonical, COUNT(*) AS n GROUP BY LOWER(application_name) HAVING COUNT(*) > 1 ORDER BY n DESC"
```

Note the `HAVING COUNT(*) > 1`, not `HAVING n > 1` — xql's `HAVING` clause resolves against source columns and aggregates, not `SELECT`-list aliases. `ORDER BY n DESC` on the next line does resolve against the alias, matching standard SQL.

Output is TSV when piped, table when interactive. To capture the output to a file:

```sh
xql apps.csv --exec "..." --mode csv --output dupes.csv
```

### 2. Bulk-close old SharePoint list items

Preview first (no `--commit`), then rerun with the flag once the sample looks right:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks \
  --exec "UPDATE SET Status = 'Closed' WHERE Modified < '2024-01-01' AND Status != 'Closed'"
```

The preview prints the affected count and a sample. Rerun with `--commit` appended to apply. In an interactive REPL, use the trailing `!` shortcut instead:

```sql
xql> UPDATE SET Status = 'Closed' WHERE Modified < '2024-01-01' AND Status != 'Closed' !
```

### 3. Schema-first exploration

Before writing anything, dump the schema so the query references real column names and types:

```sh
xql --describe apps.csv
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks --describe --all-fields
```

`--all-fields` on `sp` includes hidden/system columns (Created, Modified, Author, etc.) that are hidden by default to match what the SharePoint UI shows.

### 4. Redirect large SELECT to a file

`--output` writes SELECT results as CSV regardless of the terminal `--mode`:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Big \
  --exec "SELECT * WHERE Modified >= '2025-01-01'" \
  --output recent.csv
```

## Error patterns worth recognizing

- `unknown function: FOO` — mistyped scalar or a function xql doesn't support. Only `LOWER`, `UPPER`, `TRIM` ship today.
- `ambiguous column: X (internal names: ...)` — two SharePoint columns share a display name. Reference the internal name to disambiguate.
- `... is not supported by SharePoint: OData $filter has no equivalent for arbitrary scalar functions. Rewrite by using the column directly` — you tried `WHERE LOWER(col) = 'x'` against `sp`. Use `WHERE col ILIKE 'x'` instead.
- `LIKE pattern has no OData equivalent` — mid-pattern `%` or `_` against `sp`. Rewrite as `startswith`/`endswith`/`contains`-shaped, or fetch the column and filter client-side.
- `--exec write requires --commit` — a write in one-shot mode without the flag. Preview shipped; add `--commit` to apply.

## Not this skill's job

- Installing xql — see the README's install section (Debian/Ubuntu apt, Homebrew, curl one-liner, or `go install`).
- Building or contributing to xql — this skill is for using the binary.
- Explaining OAuth device-code flow — the first-run prompt is self-explanatory.
