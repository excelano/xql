---
name: xql
description: >-
  Run SQL against one table with `xql`: a SharePoint list (`xql sp <list-url>`), a local
  CSV/TSV (`xql data.csv`), or a hosted xinglist (`xql xinglet`, read-only). Use it to query,
  count, group, dedupe, export or bulk-update SharePoint list rows ("which items are still
  open", "bulk-update the status on these", "the list has 4,000 items"), and to filter or
  aggregate one delimited file. One table per statement: no FROM, JOIN or subquery.
  SharePoint files and folders are xfiles, not xql.
---

# xql — Excelano Query Language CLI

`xql` binds to one table at startup and runs a small SQL grammar against it. Reads run immediately; writes preview a sample of the affected rows and commit only when told to. The backends are local CSV/TSV (`csv`), a Microsoft 365 SharePoint list (`sp`), and a hosted xinglist snapshot (`xinglet`, read-only).

The binary is authoritative: `xql <backend> --help`, and `describe` inside the REPL. [GRAMMAR.md](https://github.com/excelano/xql/blob/main/GRAMMAR.md) is the full grammar and [README.md](https://github.com/excelano/xql/blob/main/README.md) the CSV dialect and type-inference rules. Where this skill disagrees with them, they win.

## When to reach for xql, and when not to

Reach for xql when the user has one CSV, one TSV, one SharePoint list or one xinglist and wants to filter, aggregate, profile, dedupe or bulk-edit it with SQL. The two lanes are not equally strong.

**SharePoint (`xql sp`)** is the best available tool for list rows. The alternatives are PnP PowerShell and raw Graph OData, both of which make you write the paging, the `$filter` translation, the field-type round-trip and the write loop by hand. None of the SQL engines likely to be installed reads a SharePoint list, so when a task outgrows the grammar here, split it into one list at a time rather than looking for another engine.

**Local CSV/TSV (`xql csv`)** is a convenience: a single-table SQL subset with careful CSV handling, enough for most one-file questions. When a question needs a JOIN, a subquery, a CTE, `UNION`, a window function or a second file, switch to a full engine. Check what is installed (`command -v duckdb sqlite3 csvsql`) rather than assuming one: DuckDB reads CSV directly, `sqlite3` takes it through `.import --csv`, and `csvsql --query` (csvkit) runs SQL over CSV files with no setup.

## The neighbours

On a **local delimited file**, xql is the last step, not the first: [xray](https://github.com/excelano/xray) profiles it read-only and names the hazards, [xled](https://github.com/excelano/xled) repairs cell values (currency stored as text, stripped leading zeros, a buried header), and [xshape](https://github.com/excelano/xshape) fixes the geometry (unpivot a wide export before you can group by year).

On **SharePoint**, xql owns list rows and columns, and the [xfiles](https://github.com/excelano/xfiles) tools own files and folders in a document library: copying, mirroring, finding and listing them. They share one sign-in.

## Feature guard

The recipes assume an xql with the scalar functions `LOWER`/`UPPER`/`TRIM` and `YEAR`/`MONTH`/`DAY`, expression `GROUP BY`, and `--describe`. An "unknown function" error on one of those names means the installed copy predates it.

They also assume that a write leaves the rows and cells it does not change as they were read. If `--describe` reports a column of 20-digit IDs as `float`, the installed copy predates that: pass `--type Col=string` for every column you are not assigning, or upgrade.

Upgrade with `sudo apt install --only-upgrade xql` (Debian/Ubuntu), `brew upgrade xql` (macOS), or the install one-liner in the README.

## Invocation

A path ending in `.csv` or `.tsv` runs the CSV backend without the subcommand, so `xql data.csv` and `xql csv data.csv` are the same. SharePoint and xinglet always need the subcommand and a URL:

```
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks
xql xinglet xinglet://4babff02-909f-4dba-b3df-3edf14b778bf
```

`-` reads a CSV from stdin, after an explicit `csv` and with `--exec` or `--describe`. A committed write from stdin needs `--output PATH`, since there is no file to write back to.

```
cat data.csv | xql csv - --exec "SELECT * WHERE qty > 10"
```

Exit `0` is success, including a query that matches no rows, so read the row count rather than the status. `1` is bad input: an unreadable source, a parse error, a rejected statement, a failed sign-in. `2` is a bad invocation: an unknown flag, a missing argument, contradictory options.

## The grammar

```
SELECT [DISTINCT] projection, ... [WHERE ...] [GROUP BY expr, ...] [HAVING ...] [ORDER BY key, ...] [LIMIT n] [OFFSET m]
UPDATE SET col = expr, ... [WHERE ...]
DELETE [WHERE ...]
INSERT (col, ...) VALUES (val, ...)
```

There is no `FROM` and no table name: `xql data.csv --exec "SELECT * WHERE Status = 'Open'"`. Ordinary SQL written with `FROM t` or `UPDATE t SET` fails to parse, and the error carries a hint saying so.

Projections take arithmetic (`price * qty AS line_total`), aggregates (`COUNT(*)`, `COUNT(DISTINCT col)`, `SUM`, `AVG`, `MIN`, `MAX`) and the scalar functions `LOWER`, `UPPER`, `TRIM`, `YEAR`, `MONTH` and `DAY`, which also work in predicates and `GROUP BY`. The left side of a comparison is an expression (`WHERE price * qty > 100`); the right side is always a literal, so `col1 = col2` is not supported. Predicates cover `=`, `!=`, `<`, `>`, `<=`, `>=`, `IS [NOT] NULL`, `[NOT] LIKE`, `[NOT] ILIKE`, `[NOT] IN (...)`, `[NOT] BETWEEN a AND b`, `AND`, `OR` and `NOT`.

`HAVING` resolves against source columns and aggregates, not `SELECT` aliases (`HAVING COUNT(*) > 1`, not `HAVING n > 1`); `ORDER BY` does resolve aliases. Keywords and column names are case-insensitive on input. GRAMMAR.md lists the smaller absences, such as expression keys in `ORDER BY` and `LENGTH`/`SUBSTRING`/`ROUND`; check it before assuming a function exists.

## Writes

Writes are dry runs unless committed. In `--exec` mode a write previews and exits without `--commit`, and a bare `DELETE` (no `WHERE`) also needs `--confirm-destructive`. In the REPL a write prompts `Apply? [y/N]:`, and a trailing `!` commits without the prompt, except for a bare `DELETE`, which always prompts. The xinglet backend rejects every write.

A committed write rewrites the bound CSV, or PATCHes each affected SharePoint item. `--output PATH` sends the modified CSV to another file instead of overwriting the source.

## Flags

`xql csv <path>`:

| Flag | Effect |
|------|--------|
| `--exec "<sql>"` | Run one statement and exit. |
| `--describe` | Print the schema and exit. |
| `--commit` | Apply a write in `--exec` mode. |
| `--confirm-destructive` | Allow a bare `DELETE` in `--exec` mode. |
| `--mode table\|tsv\|csv\|json` | Output format; table on a terminal, TSV when piped. |
| `--json` | Shorthand for `--mode json`. |
| `--no-input-header`, `--no-header` | The source has no header; columns are `col1`, `col2`, and so on. |
| `--no-output-header` | Suppress the header row in output. |
| `-d`, `--delim CHAR` | Field delimiter (`\t` for tab); tab for a `.tsv` file, comma otherwise. |
| `--type Col=int,Other=string` | Override type inference. |
| `--output PATH` | Write the result, or the committed table, to PATH as CSV. |

`xql sp <list-url>` takes the same `--exec`, `--describe`, `--commit`, `--confirm-destructive`, `--mode`, `--json`, `--no-output-header` and `--output`, plus `--all-fields` to include hidden and system columns in `SELECT *` and `--describe`.

`xql xinglet xinglet://<uuid>` takes `--exec`, `--describe`, `--mode`, `--json`, `--no-output-header` and `--output`, and reads its token from `XINGLET_TOKEN`.

## CSV types

xql infers `int`, `float`, `bool`, `date` or `string` per column from a sample, and the type decides how comparisons, sorting and arithmetic behave: `Priority > 2` is numeric only if `Priority` inferred as a number. Run `--describe` before relying on a comparison, and pin a column with `--type` when inference picked wrong. A date column of `03/04/2024` infers as `string`, and so do leading-zero codes and IDs too long for a float.

The type does not change what is written. A committed write rewrites only the rows it changes; every other row is written back as its original text, and the file keeps its BOM and line endings. In a changed row, cells the statement does not assign keep their source text, so an `UPDATE` that sets one column leaves every other cell as it was. A cell the column's type cannot parse reads as `NULL` in queries and is still written back unchanged.

## SharePoint specifics

`WHERE` translates to OData `$filter` and runs server-side; `ORDER BY`, `LIMIT`, `OFFSET` and `DISTINCT` apply client-side to the filtered set. `LIKE` maps to `startswith`, `endswith` or `contains` by where the `%` sits, and a mid-pattern `%`, a `_` wildcard or an escape is rejected because OData has no equivalent. `ILIKE` lowercases both sides with `tolower()`. `IN` becomes an OR'd equality chain and `BETWEEN` a `ge`/`le` pair.

Every column has an internal name (what Graph uses, often `field_5`) and a display name (what the SharePoint UI shows), and either resolves. `describe` prints both when they differ, and two columns sharing a display name raise an ambiguous-column error that lists the internal names.

Writes validate against the list schema before any Graph call. Person, Lookup, Hyperlink and Calculated columns are rejected on write. A Lookup field reads as its numeric ID, and that ID is what to write.

Sign-in is device-code OAuth, with the token cached in `~/.config/excelano/sp-token.json`, shared with the xfiles tools. `xql auth --json` reports `signed_in`, `account`, `tenant`, `token_expires` and `scopes` without starting a sign-in and exits 0 either way. Run it before a batch of `xql sp` calls on a machine you are unsure of, and branch on `signed_in`. If it is false, ask the user to run `xql sp <list-url>` once from their own terminal.

## Recipes

### Case-insensitive duplicates in a CSV

```sh
xql apps.csv --exec "SELECT LOWER(application_name) AS canonical, COUNT(*) AS n GROUP BY LOWER(application_name) HAVING COUNT(*) > 1 ORDER BY n DESC"
```

Add `--output dupes.csv` to capture the result as CSV.

### Count by month, and when the date column is not one

```sh
xql staff.csv --exec "SELECT YEAR(hired) AS y, MONTH(hired) AS m, COUNT(*) AS n GROUP BY YEAR(hired), MONTH(hired) ORDER BY y, m"
```

This needs `hired` to infer as `date`, which needs ISO values. On a column of `03/04/2024`, `YEAR` returns `NULL` for every row rather than guess the day order. Normalize it in xled first, then query the result:

```sh
xled '[hired] = date([hired], "DD/MM/YYYY")' staff.csv > staff-iso.csv
xql staff-iso.csv --exec "SELECT YEAR(hired) AS y, COUNT(*) AS n GROUP BY YEAR(hired)"
```

### Bulk-close old SharePoint list items

Preview, read the sample, then rerun with `--commit`:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks \
  --exec "UPDATE SET Status = 'Closed' WHERE Modified < '2024-01-01' AND Status != 'Closed'"
```

### Export a filtered list

`--output` writes CSV whatever the terminal `--mode`:

```sh
xql sp https://contoso.sharepoint.com/sites/team/Lists/Big \
  --exec "SELECT * WHERE Modified >= '2025-01-01'" --output recent.csv
```

## Errors to recognize

- `unknown function FOO` is a mistyped scalar or a capability that lives in a sibling tool, and the message names which: text rewriting routes to xled, reshaping to xshape, and xql's missing functions name the xled command that computes the column. A bare "unknown function" with no pointer is a typo.
- `ambiguous column: X (internal names: ...)` means two SharePoint columns share a display name. Use the internal name.
- `... is not supported by SharePoint: OData $filter has no equivalent ...` comes from a scalar function in `WHERE` against `sp`. Write `WHERE col ILIKE 'x'` instead of `WHERE LOWER(col) = 'x'`.
- `LIKE pattern has no OData equivalent` is a mid-pattern `%` or `_` against `sp`. Reshape the pattern, or fetch the column and filter afterwards.
- `no cached token, and no terminal is attached to complete device-code sign-in` means sign-in needs a human. Ask the user to run `xql sp <list-url>` once from their own terminal, then rerun. Do not retry or script the browser flow.

## Not this skill's job

Installing xql is in the README's install section, and building or contributing to it is outside this skill, which covers using the binary.
