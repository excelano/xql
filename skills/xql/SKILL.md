---
name: xql
description: >-
  Run SQL against one bound table with `xql` — one grammar, three backends, two lanes that
  have little in common. **SharePoint Lists (`xql sp`) — the strong lane.** Fires on "pull
  the tasks list", "which items are still open", "bulk-update the status on these", "count
  them by owner", "the list has 4,000 items and the browser view is useless", "find the
  duplicates", "export the list to CSV". It handles the OData translation, the paging and the
  typed-field round-tripping that PnP PowerShell and hand-rolled Graph calls get wrong, and
  every write previews the affected rows and prompts before committing. Prefer it outright
  here — the DuckDB caveat below does not apply, because there is no DuckDB over a SharePoint
  list. **Local CSV/TSV (`xql csv`, or just `xql data.csv`) — the convenience lane.** Filter,
  aggregate, group, dedupe or profile one delimited file. In this lane only, JOINs,
  subqueries, CTEs, UNION and window functions are permanently out of scope: use DuckDB. Also
  reads a hosted xinglist (`xql xinglet`, read-only). On a local file, profile with xray,
  repair values with xled and reshape with xshape first. For SharePoint *files and folders*
  rather than list rows, that is xfiles (`xcp`, `xftp`, `xsync`, `xfind`, `xtree`).
---

# xql — Excelano Query Language CLI

`xql` binds to one tabular resource at startup and runs a small SQL grammar against it. Reads run immediately; writes preview a sample of affected rows and prompt for confirmation before committing. One grammar, three backends: local CSV/TSV (`csv`), Microsoft 365 SharePoint List (`sp`), and a hosted xinglist snapshot (`xinglet`, read-only).

Authoritative sources for this skill are the `xql` binary itself (`xql <backend> --help`, `describe` inside the REPL) and the maintainer-authored [GRAMMAR.md](https://github.com/excelano/xql/blob/main/GRAMMAR.md) and [README.md](https://github.com/excelano/xql/blob/main/README.md). This skill mirrors those; if it conflicts with them, they win.

## When to reach for xql (and when not to)

Reach for xql when the user has one CSV, one TSV, one SharePoint list, or one xinglist, and wants to filter, aggregate, profile, dedupe, or bulk-edit it with SQL. That is the whole story. It replaces spreadsheet-in-Excel, hand-written per-transform scripts, and — for SharePoint specifically — the PnP PowerShell "loop the list and PATCH each item" pattern.

The two lanes are not equally strong, and it is worth knowing which one you are in.

**SharePoint (`xql sp`).** xql is the best available answer, not a convenience. The alternatives are PnP PowerShell and raw Graph OData, both of which make you write the paging, the `$filter` translation, the field-type round-trip and the write loop by hand. Nothing about the grammar limits below argues for going back to those — there is no DuckDB over a SharePoint list, so "reach for DuckDB instead" is not an option in this lane and should not be read as one.

**Local CSV/TSV (`xql csv`).** xql is a convenience: a single-table SQL subset with sane CSV handling, which is enough for most one-file questions and beats writing a script. When the question outgrows it, DuckDB is genuinely better and you should switch without hesitation.

Do not reach for xql if the task needs JOINs, subqueries, CTEs, `UNION`, window functions, or an expression on the right side of a comparison. Those are permanently out of scope by design — xql binds to one table per session. Over CSV, that means DuckDB; for writes across multiple tables, sqlite3 or the source system's own API. Over SharePoint it means splitting the work into one bound list at a time, since the fallback does not exist.

## The neighbours

Same file, different verb. On a **local delimited file**, xql is the last step, not the first: [xray](https://github.com/excelano/xray) profiles it read-only and names the hazards, [xled](https://github.com/excelano/xled) repairs cell values (currency trapped as text, stripped leading zeros, a buried header), and [xshape](https://github.com/excelano/xshape) fixes the geometry (unpivot a wide export before you can group by year). Querying a file none of those have seen is how a leading zero becomes an integer.

On **SharePoint**, the neighbour is a different family: xql owns *list rows and columns*, and the [xfiles](https://github.com/excelano/xfiles) tools own *files and folders* in a document library — `xcp` (copy, like scp), `xftp` (an interactive session), `xsync` (mirror a tree, like rsync), `xfind` (walk for matching paths), `xtree` (print the tree). If the task is "upload this folder to the library" or "pull down everything under /Shared Documents/Reports", that is xfiles, not xql. They share one sign-in: the same app registration and the same token cache.

## Feature guard

The recipes and flags below assume an xql with the scalar functions `LOWER`/`UPPER`/`TRIM` and `YEAR`/`MONTH`/`DAY`, expression `GROUP BY`, and `--describe`. An "unknown function" error on one of those names means the installed copy predates it; upgrade with `sudo apt install --only-upgrade xql` (Debian/Ubuntu), `brew upgrade xql` (macOS), or by re-running the install one-liner from the README. Falling back means rewriting explicitly — `SELECT column` instead of `SELECT LOWER(column)`, and grouping on the raw date instead of `YEAR(date)`.

## Dispatch — how xql picks a backend

The top-level dispatcher scans past any leading flags to find the first non-flag token, then routes on it:

1. If the token is a registered subcommand (`csv`, `sp`, `xinglet`, or `help`/`--help`/`-h`/`-V`/`--version`), that backend runs with the remaining args (leading flags preserved).
2. Otherwise, if the token has a recognized file extension (`.csv` or `.tsv`), the CSV backend runs with the full args.
3. Otherwise, error.

So these are all equivalent for a local CSV file:

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

`-` reads the table from stdin, but only after an explicit `csv`, because stdin carries no
extension for step 2 to infer from. It also needs `--exec` or `--describe`: the REPL would
otherwise be reading its commands from the stream the table just came out of. A committed
write from stdin needs `--output PATH`, since there is no source file to write back to.

```
cat data.csv | xql csv - --exec "SELECT * WHERE qty > 10"
```

## Exit codes

`0` is success, and that includes a query matching no rows — an empty result is the answer,
not a failure, so read the row count rather than the exit status. `1` is bad input: an
unreadable source, a parse error, a statement the backend rejects, a failed sign-in. `2` is a
bad invocation: an unknown flag, a missing argument, contradictory options. `--help` at any
level, including `xql csv --help`, is a success and prints to stdout.

## SQL subset — what's in and what's out

Grammar shared across every backend:

- `SELECT [DISTINCT] projection_list [WHERE ...] [GROUP BY expr, ...] [HAVING ...] [ORDER BY key, ...] [LIMIT n] [OFFSET m]`
- `UPDATE SET col = expr, ... [WHERE ...]`
- `DELETE [WHERE ...]`
- `INSERT (col, ...) VALUES (val, ...)`

Note the absence of `FROM`. The bound table is implicit — `xql csv data.csv` then `SELECT *` is enough.

Projections may include arithmetic (`price * qty AS line_total`), aggregates (`COUNT(*)`, `COUNT(DISTINCT col)`, `SUM`, `AVG`, `MIN`, `MAX`), and the scalar functions: `LOWER(s)`, `UPPER(s)`, `TRIM(s)` for string normalization, and `YEAR(d)`, `MONTH(d)`, `DAY(d)` for calendar components. Scalars can appear in the projection list, in predicates, and in `GROUP BY` (case-insensitive dedup uses this — see recipe 1). Unknown scalar names produce an "unknown function" error at plan time.

The date accessors take a `date` column or a `string` holding ISO dates; a string cell that isn't a date yields `NULL` for that row, while an `int`/`float` column is rejected before the scan with a pointer at the `--type` override. There is no serial-number reading of a numeric column.

Predicates support `=`, `!=`, `<`, `>`, `<=`, `>=`, `IS [NOT] NULL`, `[NOT] LIKE`, `[NOT] ILIKE`, `[NOT] IN (...)`, `[NOT] BETWEEN a AND b`, and boolean composition with `AND`, `OR`, `NOT`. Left side is an expression (`WHERE price * qty > 100` is fine); right side is always a literal (`col1 = col2` is not supported).

Out of scope (permanent): `JOIN`, subqueries, `UNION`/`INTERSECT`/`EXCEPT`, CTEs, window functions. Reach for DuckDB when a task needs one of these. Some smaller absences (expression keys in `ORDER BY`, multi-column `COUNT(DISTINCT a, b)`, and the scalar functions `LENGTH`/`SUBSTRING`/`LEFT`/`RIGHT`/`ROUND`) are listed in [GRAMMAR.md](https://github.com/excelano/xql/blob/main/GRAMMAR.md) rather than repeated here — check it, or `xql --help`, before assuming a function exists.

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
| `--json` | Shorthand for `--mode json`. Conflicts with any other `--mode`. |
| `--no-input-header`, `--no-header` | Source has no header; columns become `col1`, `col2`, … |
| `--no-output-header` | Suppress the header row in output. |
| `-d`, `--delim CHAR` | Field delimiter (use `\t` for tab). Defaults to tab for a `.tsv` file, comma otherwise. |
| `--type Col=int,Other=string` | Override the sampled type inference. |
| `--output PATH` | Write results (or committed table) to PATH as CSV. |

The path may be `-` for stdin; see the dispatch section for the two conditions that come with it.

`xql sp <list-url>` adds `--all-fields` (include hidden/system columns in `SELECT *` and `--describe`). Same `--exec`, `--describe`, `--commit`, `--confirm-destructive`, `--mode`, `--json`, `--no-output-header`, `--output` semantics as `csv`.

`xql xinglet xinglet://<uuid>` accepts `--exec`, `--describe`, `--mode`, `--json`, `--no-output-header`, `--output`. `XINGLET_TOKEN` must be set in the environment. No write flags — the backend is read-only.

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

Auth is device-code OAuth. First run prints a short code + URL; a refresh token is cached at `~/.config/excelano/sp-token.json` (mode 0600), one file shared with the xfiles tools, so a sign-in done with any of them covers `xql sp` too.

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

### 2. Count by month, and where the date column isn't one

`YEAR`, `MONTH`, and `DAY` turn a date column into something groupable:

```sh
xql staff.csv --exec "SELECT YEAR(hired) AS y, MONTH(hired) AS m, COUNT(*) AS n GROUP BY YEAR(hired), MONTH(hired) ORDER BY y, m"
```

This only works if the column actually inferred as `date`, which requires every non-empty value to be ISO. Check with `--describe` first. A column of `03/04/2024` infers as `string`, and `YEAR` on it returns `NULL` for every row rather than guessing whether that is March or April — xql has no more appetite for that guess than xled does.

The fix is to normalize upstream in xled and query the result, which is the family's intended shape for a two-step job:

```sh
xled '[hired] = date([hired], "DD/MM/YYYY")' staff.csv > staff-iso.csv
xql staff-iso.csv --exec "SELECT YEAR(hired) AS y, COUNT(*) AS n GROUP BY YEAR(hired)"
```

### 3. Bulk-close old SharePoint list items

Preview first (no `--commit`), then rerun with the flag once the sample looks right:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks \
  --exec "UPDATE SET Status = 'Closed' WHERE Modified < '2024-01-01' AND Status != 'Closed'"
```

The preview prints the affected count and a sample. Rerun with `--commit` appended to apply. In an interactive REPL, use the trailing `!` shortcut instead:

```sql
xql> UPDATE SET Status = 'Closed' WHERE Modified < '2024-01-01' AND Status != 'Closed' !
```

### 4. Schema-first exploration

Before writing anything, dump the schema so the query references real column names and types:

```sh
xql --describe apps.csv
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks --describe --all-fields
```

`--all-fields` on `sp` includes hidden/system columns (Created, Modified, Author, etc.) that are hidden by default to match what the SharePoint UI shows.

### 5. Redirect large SELECT to a file

`--output` writes SELECT results as CSV regardless of the terminal `--mode`:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Big \
  --exec "SELECT * WHERE Modified >= '2025-01-01'" \
  --output recent.csv
```

## Error patterns worth recognizing

- `unknown function FOO` — mistyped scalar, or a capability that lives in a sibling tool. The message says which: text rewriting (`REPLACE`) routes to xled, reshaping (`PIVOT`, `UNPIVOT`, `SPLIT_PART`) to xshape, and xql's own not-yet functions (`LENGTH`, `SUBSTRING`, `LEFT`, `RIGHT`, `ROUND`) name the xled command that computes the column so it can be queried here. A bare "unknown function" with no pointer is a plain typo.
- `ambiguous column: X (internal names: ...)` — two SharePoint columns share a display name. Reference the internal name to disambiguate.
- `... is not supported by SharePoint: OData $filter has no equivalent for arbitrary scalar functions. Rewrite by using the column directly` — you tried `WHERE LOWER(col) = 'x'` against `sp`. Use `WHERE col ILIKE 'x'` instead.
- `LIKE pattern has no OData equivalent` — mid-pattern `%` or `_` against `sp`. Rewrite as `startswith`/`endswith`/`contains`-shaped, or fetch the column and filter client-side.
- `--exec write requires --commit` — a write in one-shot mode without the flag. Preview shipped; add `--commit` to apply.
- `no cached token, and no terminal is attached to complete device-code sign-in` — you are the first caller on this machine and sign-in needs a human. It fails immediately rather than printing a code and blocking, which is the right outcome for you but means you cannot fix it yourself: ask the user to run `xql sp <list-url>` once from their own terminal to cache a token, then re-run. Do not retry, and do not try to script the browser flow.

## Not this skill's job

- Installing xql — see the README's install section (Debian/Ubuntu apt, Homebrew, curl one-liner, or `go install`).
- Building or contributing to xql — this skill is for using the binary.
- Explaining OAuth device-code flow — the first-run prompt is self-explanatory to the human who has to complete it. When there is no human (see the error patterns above), xql says so and stops.
