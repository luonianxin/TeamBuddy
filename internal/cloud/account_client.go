package cloud

import (
	"context"
	"io"
	"net/http"

	"goteams-client/internal/secrets"
)

// ForAccount binds credentials and cancellation to one login. The bootstrap
// client remains usable for login, but never participates in background sync.
func (c *Client) ForAccount(ctx context.Context, store secrets.Store, userID string) *Client {
	bound := *c
	bound.secretStore = store
	bound.userID = userID
	boundHTTP := *c.httpClient
	boundHTTP.Transport = &accountTransport{session: ctx, base: c.httpClient.Transport}
	bound.httpClient = &boundHTTP
	return &bound
}

func (c *Client) UserID() string { return c.userID }

type accountTransport struct {
	session context.Context
	base    http.RoundTripper
}

func (t *accountTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.session.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.session, cancel)
	cleanup := func() { stop(); cancel() }
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req.Clone(ctx))
	if err != nil {
		cleanup()
		return nil, err
	}
	resp.Body = &accountResponseBody{ReadCloser: resp.Body, cleanup: cleanup}
	return resp, nil
}

type accountResponseBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *accountResponseBody) Close() error {
	defer b.cleanup()
	return b.ReadCloser.Close()
}
