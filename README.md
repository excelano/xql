# xql

**XQL — Excelano Query Language.** One CLI, one SQL-shaped query language, multiple backends.

`xql` binds to a tabular data source at startup and runs SELECT, UPDATE, DELETE, and INSERT against it. Writes preview first, apply on confirmation. The grammar is shared across backends; only the I/O differs.

```
$ xql tasks.csv
Connected to: tasks.csv (5 columns, 248 rows). Type "help" for commands, "quit" to exit.

xql> SELECT Title, Status WHERE Priority > 2
| Title              | Status      |
| ------------------ | ----------- |
| Migrate auth layer | Open        |
| Backfill activity  | In Progress |
(2 rows)

xql> UPDATE SET Status = 'Done' WHERE Modified < '2024-01-01'
Would update 8 rows in tasks.csv:
  SET Status = "Done"
Sample:
| id | Title              |
| -- | ------------------ |
| 41 | Q3 invoice cleanup |
| 47 | Audit log purge    |
  ... 6 more
Apply? [y/N]: y
Updated 8 of 8 rows. Wrote tasks.csv.
```

## Why

Tabular data lives in many places — CSVs on disk, SharePoint Lists in M365 tenants, Excel exports, database tables. Editing them in bulk is awkward. Spreadsheet apps choke past a few hundred thousand rows, point-and-click web UIs are not scriptable, and writing a one-off script for each transform is overkill. `xql` is the smallest tool that lets you write one SQL statement, see what it would change, and commit if it is right — against whichever backend matches the data you have in front of you.

v1.0 shipped the CSV backend (replacing standalone [sqlcsv](https://github.com/excelano/sqlcsv)). v1.1 adds the SharePoint backend (replacing standalone [spsql](https://github.com/excelano/spsql)). The grammar is identical across backends — code written against `xql csv` runs against `xql sp` once you point it at a list.

## Install

Prebuilt binary (Linux and macOS, x86_64 and arm64):

```
curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sh
```

If the installer needs to write to a root-owned directory like `/usr/local/bin` (typical when upgrading a previously sudo-installed copy), wrap `sh`, not `curl`:

```
curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sudo sh
```

Pin to a specific version:

```
XQL_VERSION=v1.0.0 curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sh
```

Install elsewhere than `/usr/local/bin` (or `~/.local/bin` if not writable):

```
XQL_INSTALL_DIR=$HOME/bin curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sh
```

With [Homebrew](https://brew.sh) on macOS or Linux, so `brew upgrade` keeps it current:

```sh
brew tap excelano/tap
brew trust excelano/tap   # one-time: Homebrew gates third-party taps behind explicit trust
brew install xql
```

On Debian or Ubuntu, install from the [Excelano apt repository](https://excelano.com/apt/) instead, so `apt upgrade` keeps it current:

```sh
curl -fsSL https://excelano.com/apt/setup.sh | sudo sh
sudo apt install xql
```

From source (Go 1.24 or later):

```
go install github.com/excelano/xql/cmd/xql@latest
```

### Upgrade

Re-run the installer. If `xql` is already on your `PATH`, it upgrades the existing copy in place rather than scattering a duplicate into the default directory. If you explicitly set `XQL_INSTALL_DIR` to a different directory than the existing copy, the installer warns and leaves both in place — `PATH` order then decides which version runs.

### Uninstall

```
curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/uninstall.sh | sh
```

The uninstaller removes the `xql` binary it finds on `PATH` and asks before removing `~/.config/xql/` (REPL history). Run twice if you have duplicate installs in multiple directories. `XQL_UNINSTALL_YES=1` skips the binary-removal prompt but keeps the config dir — the REPL history is only removed if you also pass `XQL_PURGE=1` (or answer yes interactively).

## Backends

| Name | Extensions | Status |
|------|------------|--------|
| `csv` | `.csv`, `.tsv` | available |
| `sp`  | (never inferred — URL + auth required) | available |
| `xinglet` | (never inferred — `xinglet://` URL + Bearer token required) | available (read-only) |

`xql --help` lists registered backends. `xql <backend> --help` shows backend-specific flags.

### Dispatch rules

1. If `argv[1]` matches a registered backend name, route to that backend with `argv[2:]`.
2. Otherwise, if `argv[1]` has a recognized file extension, route to the matching backend with `argv[1:]`.
3. Otherwise, error.

No content sniffing. A missing or unknown extension is a usage error — fall back to the explicit subcommand.

## Usage

### Interactive REPL

```
xql csv <path>
xql <path>           # equivalent when the extension is .csv or .tsv
```

Opens a prompt bound to the file. Arrow keys recall history, Ctrl-R searches it, Ctrl-D exits. History persists at `~/.config/xql/history-csv` across sessions (one history file per backend).

The REPL accepts SQL statements one per line plus a few meta-commands as plain words (case-insensitive): `help` or `?` shows command help, `describe` prints the column schema with inferred types (`describe all` on `xql sp` includes the SharePoint system columns hidden by default), `refresh` re-reads the file from disk, and `quit` or `exit` leaves the REPL. Output controls follow sqlite shapes (without the leading dot): `mode <table|tsv|csv|json>` sets how results render to stdout, `headers on|off` toggles the column-name row, `output 'PATH'` redirects subsequent SELECT results to PATH as CSV (sticky — type `output` with no argument to clear), and `once 'PATH'` redirects only the next statement. Runtime toggles use `set <name> on|off`; today `set all-fields on` includes hidden SharePoint columns in `SELECT *`, and bare `set` lists the current state.

Writes (INSERT, UPDATE, DELETE) preview by default. `xql` prints the affected count, a sample of the rows that match, and then prompts `Apply? [y/N]:`. Anything but `y` cancels. Append `!` to skip the prompt and commit immediately:

```sql
UPDATE SET Status = 'Done' WHERE Modified < '2024-01-01' !
```

When a write is applied, `xql` rewrites the bound file. Pass `--output FILE` at startup (or use the `output` REPL command) to redirect both committed writes and SELECT results to a different file. `--output` always serializes CSV regardless of `--mode`; `--mode` controls the terminal view only.

### One-shot mode

```
xql csv <path> --exec "<sql>"
```

Runs one statement and exits. Writes need `--commit`; a bare DELETE (no WHERE clause) additionally needs `--confirm-destructive`. Output auto-detects to ASCII table on an interactive terminal and TSV when piped. Override with `--mode=csv` for RFC 4180 CSV, `--mode=json` for JSON, or pass `--no-output-header` to drop the header row in any row-shaped mode.

### CSV dialect

By default, the CSV backend expects a header row, comma delimiter, double-quote quoting, and UTF-8. Override with:

- `--no-input-header` — file has no header; columns are named `col1`, `col2`, ...
- `--delim CHAR` — single-character delimiter other than `,` (use `\t` for tab)

A UTF-8 byte-order mark (BOM) at the start of the file — common in Excel's "Save as CSV UTF-8" output — is stripped automatically; the first column name is not prefixed with it. CRLF and LF line endings are both accepted. Fields containing the delimiter, embedded quotes, or embedded newlines work as long as they are properly double-quoted per RFC 4180.

If the file looks like UTF-7 (the `+ACI-` escape that Scoutbook exports emit) or carries a UTF-16 BOM, `xql` prints a warning at startup with the `iconv` command needed to convert it to UTF-8, then proceeds. Detection is done from byte-perfect signatures only — no encoding-guessing heuristic — so a false positive on a real UTF-8 file is vanishingly unlikely.

Parsing uses `LazyQuotes = true`, which is forgiving about bare quotes mid-field and unbalanced quotes — usually a good thing for messy real-world files, but it can mask data corruption in a CSV that was truncated mid-export. A row count that does not match what you expect is the symptom.

Headers are trimmed of leading and trailing whitespace; the load fails clearly if a header is empty or duplicates another header, since both quietly corrupt schema lookups.

### Type inference

`xql` samples the first 1024 rows of a CSV and infers a type per column: `int`, `float`, `bool`, `date`, or `string`. Comparisons use the inferred type, so `Priority > 2` does numeric compare and `Modified < '2024-01-01'` does date compare. The `describe` command shows what was inferred. Override at startup with `--type Name=string,Priority=int` if inference picks wrong.

A few inference behaviors are worth knowing:

- **Leading-zero values stay strings.** `"07030"`, `"007"`, `"-01"` look numeric to `strconv` but are almost always identifiers (ZIP codes, employee numbers, phone extensions). Inferring them as `int` would silently drop the leading zero on the next write, so the column infers as `string`. Pass `--type Code=int` to override.
- **`NaN` and `Inf` are not treated as numeric.** Excel's `#DIV/0!`-as-`NaN` cells leak through `strconv.ParseFloat`, but `NaN` breaks SQL equality (NaN ≠ NaN) and pollutes round-trips, so the column falls back to `string` whenever they appear.
- **Scientific notation in the data still infers as `float`.** If you have integer IDs that Excel rendered as `1.23E+12`, the round-trip will not restore the original integer string. Pin the column with `--type ID=string` to preserve the literal text.

### SharePoint backend

```
xql sp https://contoso.sharepoint.com/sites/team/Lists/Tasks
```

The SharePoint backend binds to a single list and runs the same SQL grammar against it via Microsoft Graph. Authentication is device-code OAuth: the first run prints a short code and a URL to enter it at, and a refresh token is cached at `~/.config/xql/sp-token.json` (file mode 0600) so subsequent runs reauthenticate silently. The cached token is per-account; it carries `Sites.ReadWrite.All` delegated permission.

`WHERE` predicates translate to OData `$filter` and run server-side, so even large lists return quickly. `ORDER BY`, `LIMIT`, `OFFSET`, and `DISTINCT` apply client-side after the filtered fetch. `LIKE` and `ILIKE` patterns translate to OData `startswith`, `endswith`, and `contains`; the underscore wildcard and mid-pattern `%` aren't expressible in OData and are rejected with a clear error rather than silently working incorrectly. `IN` expands to an `or` chain, and `BETWEEN` to a `ge`/`le` pair.

`UPDATE`, `DELETE`, and `INSERT` validate against the list's column schema before any Graph round-trip: unknown columns, type mismatches, writes to read-only or system fields, and writes to Person/Lookup/Hyperlink/Calculated columns all fail fast. Writes preview a sample of affected rows and prompt `Apply? [y/N]:` in the REPL or require `--commit` in one-shot mode. A bare `DELETE` (no `WHERE`) in one-shot mode additionally requires `--confirm-destructive`. In the REPL, bare `DELETE` always prompts even with a trailing `!` shortcut.

History persists at `~/.config/xql/history-sp`. The list URL can be the bare list root or an AllItems.aspx variant; URL-encoded list-name segments are decoded automatically. Pass `--all-fields` to include hidden and system columns in `SELECT *`; by default the REPL hides them, matching what the SharePoint UI shows.

### Xinglet backend

```
xql xinglet xinglet://4babff02-909f-4dba-b3df-3edf14b778bf
```

The xinglet backend reads a remote [xinglist](https://xinglet.com) over HTTPS and pipes the CSV body through the same loader and executor as `xql csv`. Authentication is a single Bearer token; mint one at [xinglet.com/home/tokens.php](https://xinglet.com/home/tokens.php) and export it before running:

```sh
export XINGLET_TOKEN=xglt_...
xql xinglet xinglet://<uuid>
```

`XINGLET_BASE_URL` overrides the server host if you self-host xinglet on a different domain (default `https://xinglet.com`). The token is sent only on the URL named on the command line — `xql` does not store, log, or persist it.

The backend is **read-only**: only `SELECT` is supported. `INSERT`, `UPDATE`, and `DELETE` are rejected with a clear error, since the server exposes no write endpoint over Bearer auth. `refresh` re-fetches the list and rebuilds the table — useful for catching upstream edits without restarting the REPL. History persists at `~/.config/xql/history-xinglet`.

The xinglist export carries inline column type annotations (`Count:number`, `Joined:date`, `Status:choice(active|inactive)`) which the backend translates to xql type hints automatically — queries reference the bare column name (`Count`, `Joined`, `Status`) and comparisons run against the correct type.

## SQL subset

`xql` implements a deliberately small SQL grammar: `SELECT` and DML with literal values, simple `WHERE` predicates, aggregates, `GROUP BY`, `HAVING`, `ORDER BY`, `LIMIT`, `OFFSET`. No JOINs, no subqueries. The same grammar applies across all backends; backend-specific differences (OData translation, identifier resolution, type coercion, read-only mode for `xql xinglet`) are noted inline. See [GRAMMAR.md](GRAMMAR.md) for the full formal grammar and semantics.

Column names are case-insensitive on input — `select * where firstname = 'John'` resolves against a `Firstname` header. Output preserves the canonical header case. If a schema carries two columns that differ only in case (`ID` and `id`), referencing either form returns an ambiguous-column error rather than guessing.

On the SharePoint backend, columns can be referenced by either their internal name (what Graph uses for `$filter` and PATCH) or their display name (what the SharePoint UI shows). `describe` lists both side by side when they differ. CSV imports leave you with internal names like `field_5` and display names taken from the CSV header — `select vendor` and `select field_5` resolve to the same column. Use `describe all` to include SharePoint's system/hidden columns, and `set all-fields on` to include them in `SELECT *` at runtime (the launch flag `--all-fields` does the same).

## Security

`xql csv` runs locally and only touches files your OS user already has access to; it makes no network calls. `xql sp` calls Microsoft Graph over HTTPS using a device-code OAuth flow and caches the resulting refresh token at `~/.config/xql/sp-token.json` (mode 0600). `xql xinglet` calls the xinglist export endpoint over HTTPS with `Authorization: Bearer $XINGLET_TOKEN`; the token is never persisted by `xql` (it lives only in your shell environment for the lifetime of the process). See [SECURITY.md](SECURITY.md) for the full policy and the vulnerability reporting process. If your organization restricts user consent, [ADMINS.md](ADMINS.md) has everything your IT department needs to review and approve the SharePoint backend.

## License

MIT. See [LICENSE](LICENSE).
