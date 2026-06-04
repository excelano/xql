package sp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// GraphClient is a thin authenticated HTTP wrapper for the small Graph surface
// the SharePoint backend uses. Token refresh is delegated to MSAL on every
// request.
type GraphClient struct {
	msal       public.Client
	account    public.Account
	httpClient *http.Client
}

func NewGraphClient(msal public.Client, account public.Account) *GraphClient {
	return &GraphClient{
		msal:       msal,
		account:    account,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// token returns a fresh access token, refreshing silently via the cached refresh
// token if needed.
func (g *GraphClient) token(ctx context.Context) (string, error) {
	result, err := g.msal.AcquireTokenSilent(ctx, defaultScopes, public.WithSilentAccount(g.account))
	if err != nil {
		return "", fmt.Errorf("acquiring token: %w", err)
	}
	return result.AccessToken, nil
}

// get issues an authenticated GET. path is everything after graphBaseURL.
func (g *GraphClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := graphBaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return g.doWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	})
}

// patch issues an authenticated PATCH with a JSON body.
func (g *GraphClient) patch(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return g.bodyReq(ctx, http.MethodPatch, path, body)
}

// post issues an authenticated POST with a JSON body.
func (g *GraphClient) post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return g.bodyReq(ctx, http.MethodPost, path, body)
}

// delete issues an authenticated DELETE.
func (g *GraphClient) delete(ctx context.Context, path string) error {
	_, err := g.doWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodDelete, graphBaseURL+path, nil)
	})
	return err
}

func (g *GraphClient) bodyReq(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request body: %w", err)
	}
	return g.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, graphBaseURL+path, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// getAll follows @odata.nextLink and returns the concatenated value array as
// raw JSON messages. Caller unmarshals each entry as needed.
func (g *GraphClient) getAll(ctx context.Context, path string, query url.Values) ([]json.RawMessage, error) {
	nextURL := graphBaseURL + path
	if len(query) > 0 {
		nextURL += "?" + query.Encode()
	}

	var all []json.RawMessage
	for nextURL != "" {
		body, err := g.doWithRetry(ctx, func() (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		})
		if err != nil {
			return nil, err
		}
		var page struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding paginated response: %w", err)
		}
		all = append(all, page.Value...)
		nextURL = page.NextLink
	}
	return all, nil
}

// doWithRetry runs the request through MSAL auth and handles 429 backoff.
// The build closure produces a fresh *http.Request on each attempt so request
// bodies remain readable on retry.
func (g *GraphClient) doWithRetry(ctx context.Context, build func() (*http.Request, error)) ([]byte, error) {
	const maxRetries = 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		tok, err := g.token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		// Required for ad-hoc $filter / $orderby on SharePoint list-item
		// fields; ignored by other Graph endpoints. Without it, any
		// fields/<col> filter returns 400 invalidRequest.
		req.Header.Set("Prefer", "HonorNonIndexedQueriesWarningMayFailRandomly")

		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP request: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryAfter(resp.Header.Get("Retry-After"))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("graph %s %s returned %d: %s", req.Method, req.URL.Path, resp.StatusCode, string(body))
		}
		return body, nil
	}
	return nil, fmt.Errorf("graph request exhausted retries")
}

func retryAfter(h string) time.Duration {
	if h == "" {
		return 5 * time.Second
	}
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
		return 0
	}
	return 5 * time.Second
}
