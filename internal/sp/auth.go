// Package sp is the SharePoint backend for xql: device-code OAuth via MSAL,
// Microsoft Graph HTTP client, list binding (site + list resolution + column
// schema), and (in later slices) OData translation and read/write executor.
//
// Package-private helpers (token, get, doWithRetry, parseListURL, ...) are kept
// lowercase because the slices that follow add files to this same package and
// will need them. Anything called from cmd/xql is exported.
package sp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// Azure app registration "Excelano SharePoint tools (xql, xftp)"
// (client 13be0775-ed76-4407-bb2c-b7a07a189bf6), multi-tenant, in Excelano's
// tenant. Shared by xql and xftp so consent state carries across both tools.
// To use your own registration instead, change this constant and rebuild —
// see SELF-HOSTING.md in the xql repo (forthcoming).
const (
	defaultClientID  = "13be0775-ed76-4407-bb2c-b7a07a189bf6"
	defaultAuthority = "https://login.microsoftonline.com/common"
)

// defaultScopes lists only resource-specific scopes. MSAL Go automatically
// appends openid, offline_access, and profile via AppendDefaultScopes, and
// flags any user-supplied scope absent from the response as "declined".
// Since Azure doesn't echo offline_access back in the scope claim (it's a
// modifier that just unlocks refresh-token issuance), adding it here triggers
// a spurious "declined scopes" failure even when refresh works correctly.
var defaultScopes = []string{
	"https://graph.microsoft.com/Sites.ReadWrite.All",
}

// fileCache persists MSAL's token cache to a single JSON file with restrictive
// permissions. The file format is opaque (managed by MSAL); we just shuttle
// bytes.
type fileCache struct {
	path string
}

func newFileCache(path string) *fileCache {
	return &fileCache{path: path}
}

func (c *fileCache) Replace(ctx context.Context, target cache.Unmarshaler, hints cache.ReplaceHints) error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading token cache: %w", err)
	}
	return target.Unmarshal(data)
}

func (c *fileCache) Export(ctx context.Context, source cache.Marshaler, hints cache.ExportHints) error {
	data, err := source.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling token cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}
	return os.WriteFile(c.path, data, 0600)
}

// NewPublicClient constructs the MSAL public client used for both silent and
// device code token acquisition. tokenCachePath is where refresh tokens are
// persisted across runs; the cmd/xql layer owns the path (typically
// ~/.config/xql/sp-token.json) so sp doesn't need to know where xql's config
// lives.
func NewPublicClient(tokenCachePath string) (public.Client, error) {
	c, err := public.New(
		defaultClientID,
		public.WithAuthority(defaultAuthority),
		public.WithCache(newFileCache(tokenCachePath)),
	)
	if err != nil {
		return public.Client{}, fmt.Errorf("creating MSAL public client: %w", err)
	}
	return c, nil
}

// Authenticate returns a usable AuthResult, attempting silent refresh against
// any cached account first and falling back to interactive device code flow.
// Device code instructions are printed to stderr so they don't pollute
// stdout-bound results.
func Authenticate(ctx context.Context, client public.Client) (public.AuthResult, error) {
	accounts, err := client.Accounts(ctx)
	if err == nil && len(accounts) > 0 {
		result, err := client.AcquireTokenSilent(ctx, defaultScopes, public.WithSilentAccount(accounts[0]))
		if err == nil {
			return result, nil
		}
		// Silent failed (refresh token expired, scopes changed, account
		// invalidated). Fall through to device code.
	}

	dc, err := client.AcquireTokenByDeviceCode(ctx, defaultScopes)
	if err != nil {
		return public.AuthResult{}, fmt.Errorf("initiating device code flow: %w", err)
	}

	fmt.Fprintln(os.Stderr, dc.Result.Message)

	result, err := dc.AuthenticationResult(ctx)
	if err != nil {
		return public.AuthResult{}, fmt.Errorf("device code authentication: %w", err)
	}
	return result, nil
}

// aadstsHints maps the most common AADSTS error codes to one-line actionable
// guidance. Keys are matched as substrings against the auth error message.
var aadstsHints = map[string]string{
	"AADSTS70002":   "Public client flows are disabled in the App Registration. Azure portal → Authentication → Allow public client flows → Yes.",
	"AADSTS7000218": "Public client flows are disabled in the App Registration. Azure portal → Authentication → Allow public client flows → Yes.",
	"AADSTS65001":   "User or admin has not consented to the application. Azure portal → API permissions → Grant admin consent.",
	"AADSTS50105":   "Admin consent is required for one or more permissions. Azure portal → API permissions → Grant admin consent.",
	"AADSTS50194":   "App is not registered as multi-tenant in this tenant. Re-check the App Registration's supported account types.",
	"AADSTS90094":   "Admin consent required for the requested permissions. Azure portal → API permissions → Grant admin consent.",
	"AADSTS900561":  "Token request endpoint mismatch. If self-hosting, verify the App Registration matches defaultClientID in internal/sp/auth.go.",
}

// HintForAuthError returns a "\nHint (CODE): …" string suffix matching the
// first AADSTS code found in err's message, or "" if none match (or err is
// nil). Codes are tested in length-descending order so AADSTS7000218 is
// matched ahead of the prefix-shared AADSTS70002.
func HintForAuthError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for _, code := range aadstsCodesByLength {
		if strings.Contains(s, code) {
			return fmt.Sprintf("\nHint (%s): %s", code, aadstsHints[code])
		}
	}
	return ""
}

// aadstsCodesByLength holds aadstsHints' keys sorted longest-first so we
// match the most specific code when one is a prefix of another.
var aadstsCodesByLength = sortedAADSTSCodes()

func sortedAADSTSCodes() []string {
	codes := make([]string, 0, len(aadstsHints))
	for c := range aadstsHints {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return len(codes[i]) > len(codes[j]) })
	return codes
}
