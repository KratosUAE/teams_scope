package graph

import (
	"context"
	"fmt"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"golang.org/x/sync/singleflight"
)

// graphScope is the only OAuth scope we ever request — application
// permissions are gated entirely by Azure AD admin consent on
// CallRecords.Read.All, not by per-call scopes.
const graphScope = "https://graph.microsoft.com/.default"

// authorityTemplate is the v2 endpoint MSAL expects. The trailing slash is
// required by msal-go's URL parser.
const authorityTemplate = "https://login.microsoftonline.com/%s/"

// tokenProvider is the minimal contract callrecords.Client uses to fetch a
// bearer token. Defining it here lets tests inject a stub without spinning
// up MSAL — see callrecords_test.go.
type tokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// TokenSource wraps an MSAL confidential.Client configured for the
// client-credentials flow. It is safe for concurrent use: MSAL maintains its
// own internal token cache, so AcquireTokenSilent is called without any lock
// (warm-cache reads are fully concurrent). On a cache miss, singleflight
// coalesces concurrent goroutines into a single AcquireTokenByCredential call
// so we never fan-out requests to the IDP under crawler parallelism.
type TokenSource struct {
	cca    confidential.Client
	scopes []string

	sf singleflight.Group
}

// NewTokenSource builds a TokenSource from raw tenant/client credentials.
// The MSAL client itself is created eagerly so configuration errors surface
// at startup, not on the first Graph call.
func NewTokenSource(_ context.Context, tenantID, clientID, clientSecret string) (*TokenSource, error) {
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("graph: %w: tenantId/clientId/clientSecret required", ErrAuth)
	}
	cred, err := confidential.NewCredFromSecret(clientSecret)
	if err != nil {
		return nil, fmt.Errorf("graph: %w: cred from secret: %v", ErrAuth, err)
	}
	authority := fmt.Sprintf(authorityTemplate, tenantID)
	cca, err := confidential.New(authority, clientID, cred)
	if err != nil {
		return nil, fmt.Errorf("graph: %w: msal new: %v", ErrAuth, err)
	}
	return &TokenSource{
		cca:    cca,
		scopes: []string{graphScope},
	}, nil
}

// Token returns a valid bearer token, fetching a fresh one only when MSAL's
// cache is empty or expired. The silent-first / credential-fallback dance is
// the documented pattern for confidential clients.
//
// AcquireTokenSilent is called without any lock — the MSAL client is
// internally thread-safe, and serializing warm-cache reads would limit
// concurrency under crawler parallelism. On a cache miss, singleflight.Group
// coalesces concurrent credential acquisitions into a single IDP call; all
// waiting goroutines share the result.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	// Fast path: cache hit — no coordination needed.
	if res, err := t.cca.AcquireTokenSilent(ctx, t.scopes); err == nil {
		return res.AccessToken, nil
	}

	// Slow path: cache miss — coalesce concurrent IDP calls by scope key.
	key := t.scopes[0] // graphScope is always the single element
	v, err, _ := t.sf.Do(key, func() (any, error) {
		res, err := t.cca.AcquireTokenByCredential(ctx, t.scopes)
		if err != nil {
			return nil, fmt.Errorf("graph: %w: acquire by credential: %v", ErrAuth, err)
		}
		return res.AccessToken, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// Compile-time guarantee that *TokenSource satisfies the tokenProvider
// interface used by Client.
var _ tokenProvider = (*TokenSource)(nil)
