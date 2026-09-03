# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories at https://github.com/excelano/xql/security/advisories/new. If you would rather not use GitHub, email david.anderson@excelano.com instead. I aim to respond within seven days.

Please do not open public issues for security problems.

## Supported versions

The latest v1.x release receives security fixes. Older versions are not supported.

## What xql can access

xql is a CLI that runs locally on your machine. Each backend is described below; run `xql --help` to see which ones your build offers.

The CSV backend (`xql csv`) reads the file you point it at, holds it in memory for the duration of the session, and writes the modified file back when you commit a write statement. It makes no network calls, has no auth layer, and can only read and write files your operating-system user already has access to.

The SharePoint backend (`xql sp`) calls Microsoft Graph over HTTPS to read and write items in a single bound SharePoint list. Authentication is delegated device-code OAuth against your Microsoft Entra ID account; the scope requested is `Sites.ReadWrite.All`. xql cannot access any data your account cannot already access in SharePoint Online. No other Graph endpoints are touched.

The xinglet backend (`xql xinglet`) fetches a snapshot of one xinglist over HTTPS from the host named by `XINGLET_BASE_URL` (by default `https://xinglet.com`), authenticating with the `XINGLET_TOKEN` environment variable as an `Authorization: Bearer` header. The token is sent only to the URL named on the command line, and xql never stores, logs, or persists it — it lives in your shell environment for the lifetime of the process. The backend is read-only: `INSERT`, `UPDATE`, and `DELETE` are rejected before parsing, because the server exposes no write endpoint over Bearer auth.

IT administrators evaluating the SharePoint backend for a Microsoft 365 tenant will find the application's registration details, the delegated-permission risk profile, and the consent and revocation steps in [ADMINS.md](ADMINS.md).

## What xql stores

xql stores REPL command history at `~/.config/xql/history-csv` and `~/.config/xql/history-sp` with file mode 0600 (directory mode 0700). The SharePoint backend additionally caches a refresh token at `~/.config/excelano/sp-token.json` (mode 0600), a file shared with the xfiles tools because they sign in against the same app registration, so subsequent runs of any tool in the family reauthenticate without another device-code prompt. The cache is written through a temp file and rename so an interrupted write never replaces a good cache. Versions before the cache was shared kept the token at `~/.config/xql/sp-token.json`; the new version adopts that file on first run and leaves it in place, and the uninstaller's purge step removes it. Delete the shared file to force re-authentication for the whole family; revoke the granted permission at https://myaccount.microsoft.com/applications to invalidate the token server-side. There is no telemetry, no analytics, and no remote logging.

## Verifying releases

Every GitHub release includes a `checksums.txt` file listing SHA-256 hashes of all binary archives. Verify any download before running it:

    sha256sum xql_1.0.0_linux_amd64.tar.gz
    # compare against the value in checksums.txt

Release artifacts are built by GitHub Actions from a tagged commit using the goreleaser configuration in this repo. The workflow and build configuration are public and auditable.
