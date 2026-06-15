# xql for IT administrators

This page is for the person who approves applications in a Microsoft 365 tenant. If a colleague has asked you to allow xql, everything you need to make that decision is below.

xql is a command-line tool that runs SQL-shaped queries against tabular data. Most of what it does never touches your tenant: its CSV backend reads and writes local files only and makes no network calls. Only the SharePoint backend (`xql sp`) reaches Microsoft 365, where it runs queries against a single SharePoint list over the Microsoft Graph API. It runs locally on the user's own machine, signs in as that user through Microsoft's device-code flow, and uses no server, daemon, background process, or service account. It can do nothing the signed-in user could not already do in SharePoint Online through a browser.

## The application you would be approving

The SharePoint backend authenticates through a multi-tenant Microsoft Entra application published in Excelano's tenant. Match it on the Application (client) ID rather than the display name, since the ID is the value that cannot be spoofed.

| Property | Value |
|---|---|
| Display name | Excelano SharePoint tools |
| Verified publisher | Excelano LLC (Microsoft-verified) |
| Application (client) ID | `13be0775-ed76-4407-bb2c-b7a07a189bf6` |
| Supported account types | Multi-tenant (accounts in any organization) |
| Client type | Public client (no client secret; device-code OAuth) |
| Requested permission | Microsoft Graph `Sites.ReadWrite.All` |
| Permission type | Delegated |

Excelano LLC is a Microsoft-verified publisher, so the consent screen and the enterprise application record both show the publisher name with a verified badge rather than the unverified-publisher warning. The same registration backs xql's sibling tool, [xftp](https://github.com/excelano/xftp), so a single consent decision covers both. Granting or blocking one grants or blocks the other.

## What the delegated permission means

The permission is delegated, not application. That distinction is the whole risk profile. A delegated permission only ever acts on behalf of the user who signed in, and only for the duration of their token. xql cannot read or write any site, list, or item that the user could not already reach themselves, and it has no standalone or unattended access — nothing happens unless that specific person has run the tool and signed in. There is no client secret embedded in the tool and no app-only token path, so the registration cannot be used for background data access.

`Sites.ReadWrite.All` is the narrowest single Graph scope that allows reading a list's schema and items and committing writes back across the SharePoint sites the user can access. xql requests no other scopes and calls no Graph endpoints beyond the bound list. The CSV backend has no auth layer at all.

## Whether you need to do anything

By default this scope is user-consentable, so in many tenants the first person to run `xql sp` simply clears a one-time consent prompt themselves and no administrator action is required. You only need to step in if your tenant restricts user consent — for example if user consent is turned off entirely, or limited to permissions classified as low impact. In that case the user will see a "needs admin approval" message instead of a consent screen, and one of the two paths below grants approval for everyone.

## Granting consent through the Entra admin center

This path lets you read the exact permission on screen before you accept it.

1. Have the user run `xql sp` once and start the sign-in. The first device-code attempt registers the application in your tenant as an enterprise application, even when consent is then blocked, which is what makes it findable in the next step.
2. Sign in to the [Microsoft Entra admin center](https://entra.microsoft.com) as a Global Administrator, Privileged Role Administrator, Cloud Application Administrator, or Application Administrator.
3. Go to Identity, then Applications, then Enterprise applications, and search for the Application ID `13be0775-ed76-4407-bb2c-b7a07a189bf6`.
4. Open Permissions and choose "Grant admin consent for *your organization*". Review the single delegated permission, Microsoft Graph `Sites.ReadWrite.All`, and accept.

The same blade exists in the Azure portal under Microsoft Entra ID if you prefer it. Once consent is recorded, users sign in with the normal device-code flow and see no further prompt.

## Granting consent through the admin-consent URL

As a one-step alternative that both registers the application and grants tenant-wide consent, an administrator can open this URL, sign in, and accept:

```
https://login.microsoftonline.com/common/adminconsent?client_id=13be0775-ed76-4407-bb2c-b7a07a189bf6
```

Because xql is a public client with no web reply URL, the browser may land on a blank or error page after you accept. The consent is still recorded; the error is only the post-consent redirect having nowhere to return to.

## Limiting who can use it

If you want xql available to some people but not the whole tenant, open the enterprise application's Properties, set "Assignment required" to Yes, and then add the specific users or groups under Users and groups. Anyone not assigned is refused at sign-in. Conditional Access policies that target the application apply as they would to any other enterprise app.

## Reviewing and revoking

After consent the application appears permanently under Enterprise applications, where its sign-in logs show exactly who has used it and when. To revoke access for the whole tenant, delete the enterprise application (this removes the consent and the service principal), or set "Enabled for users to sign in" to No under Properties to block it without deleting the record. Either action takes effect for both xql and xftp. An individual user can revoke their own grant at any time at <https://myaccount.microsoft.com/applications>.

## Reporting a concern

Security contact and reporting process are in [SECURITY.md](SECURITY.md).
