# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories at https://github.com/excelano/xql/security/advisories/new. If you would rather not use GitHub, email david.anderson@excelano.com instead. I aim to respond within seven days.

Please do not open public issues for security problems.

## Supported versions

The latest v1.x release receives security fixes. Older versions are not supported.

## What xql can access

xql is a CLI that runs locally on your machine. It ships two backends as of v1.1.

The CSV backend (`xql csv`) reads the file you point it at, holds it in memory for the duration of the session, and writes the modified file back when you commit a write statement. It makes no network calls, has no auth layer, and can only read and write files your operating-system user already has access to.

The SharePoint backend (`xql sp`) calls Microsoft Graph over HTTPS to read and write items in a single bound SharePoint list. Authentication is delegated device-code OAuth against your Microsoft Entra ID account; the scope requested is `Sites.ReadWrite.All`. xql cannot access any data your account cannot already access in SharePoint Online. No other Graph endpoints are touched.

## What xql stores

xql stores REPL command history at `~/.config/xql/history-csv` and `~/.config/xql/history-sp` with file mode 0600 (directory mode 0700). The SharePoint backend additionally caches a refresh token at `~/.config/xql/sp-token.json` (mode 0600) so subsequent runs reauthenticate without another device-code prompt. Delete that file to force re-authentication; revoke the granted permission at https://myaccount.microsoft.com/applications to invalidate the token server-side. There is no telemetry, no analytics, and no remote logging.

## Verifying releases

Every GitHub release includes a `checksums.txt` file listing SHA-256 hashes of all binary archives. Verify any download before running it:

    sha256sum xql_1.0.0_linux_amd64.tar.gz
    # compare against the value in checksums.txt

Release artifacts are built by GitHub Actions from a tagged commit using the goreleaser configuration in this repo. The workflow and build configuration are public and auditable.
