# xql

**XQL — Excelano Query Language.** One CLI, one query language, many backends.

`xql` runs a SQL-shaped query language against pluggable tabular backends. The grammar is shared; only the I/O differs.

```
xql csv data.csv                     # local CSV (or TSV)
xql sp  --site X --list Y            # SharePoint list (auth required)
xql data.csv                         # same as `xql csv data.csv`
```

## Status

Work in progress — slice 1 (scaffold + dispatcher) lands the routing layer. Real backends ship in subsequent slices.

xql v1.0 will deliver the CSV backend with parity to `sqlcsv` v2.0. xql v1.1 will add the SharePoint backend with parity to `spsql`. Both predecessor tools will be archived after v1.1.

## Backends

| Name | Extensions | Status |
|------|------------|--------|
| `csv` | `.csv`, `.tsv` | slice 4 |
| `sp`  | (never inferred — URL + auth required) | xql v1.1 |

`xql --help` lists registered backends and the dispatch rules. `xql <backend> --help` shows backend-specific flags.

## Dispatch rules

1. If `argv[1]` matches a registered backend name, route to that backend with `argv[2:]`.
2. Otherwise, if `argv[1]` has a recognized file extension, route to the matching backend with `argv[1:]`.
3. Otherwise, error.

No content sniffing. A missing or unknown extension is a usage error — fall back to the explicit subcommand.

## License

MIT — see [LICENSE](LICENSE).
