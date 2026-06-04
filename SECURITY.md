# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories at https://github.com/excelano/xql/security/advisories/new. If you would rather not use GitHub, email david.anderson@excelano.com instead. I aim to respond within seven days.

Please do not open public issues for security problems.

## Supported versions

The latest v1.x release receives security fixes. Older versions are not supported.

## What xql can access

xql is a CLI that runs locally on your machine. v1.x ships the CSV backend (`xql csv`); the SharePoint backend (`xql sp`) is a stub in v1.x and is not yet implemented. The CSV backend reads the file you point it at, holds it in memory for the duration of the session, and writes the modified file back when you commit a write statement. v1.x makes no network calls of any kind, has no auth layer, and does not implement administrative operations. It can only read and write files your operating-system user already has access to.

When the SharePoint backend ships in a later release, this policy will be updated to describe its credential handling and network access.

## What xql stores

xql stores REPL command history at `~/.config/xql/history-csv` with file mode 0600 (directory mode 0700). That is everything: no telemetry, no analytics, no remote logging.

## Verifying releases

Every GitHub release includes a `checksums.txt` file listing SHA-256 hashes of all binary archives. Verify any download before running it:

    sha256sum xql_1.0.0_linux_amd64.tar.gz
    # compare against the value in checksums.txt

Release artifacts are built by GitHub Actions from a tagged commit using the goreleaser configuration in this repo. The workflow and build configuration are public and auditable.
