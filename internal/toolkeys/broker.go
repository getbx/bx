package toolkeys

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxBodyBytes = 8 << 20

type Broker struct {
	store  *Store
	audit  *Audit
	client *http.Client
}

func NewBroker(store *Store, audit *Audit, client *http.Client) *Broker {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Broker{store: store, audit: audit, client: &clone}
}
func (b *Broker) Do(ctx context.Context, in APIRequest, surface string) (APIResponse, error) {
	c, err := b.store.Resolve(in.CredentialID)
	if err != nil {
		return APIResponse{}, &Error{Code: CodeCredentialRequired, Message: err.Error()}
	}
	if !c.Enabled {
		return APIResponse{}, &Error{Code: CodeCredentialDisabled, Message: "credential disabled"}
	}
	if !strings.HasPrefix(in.Path, "/") || strings.HasPrefix(in.Path, "//") || strings.ContainsAny(in.Path, "\r\n") {
		return APIResponse{}, &Error{Code: CodeRequestInvalid, Message: "path must be relative"}
	}
	u, err := url.Parse(c.Origin)
	if err != nil {
		return APIResponse{}, err
	}
	u.Path = in.Path
	u.RawQuery = in.Query.Encode()
	req, err := http.NewRequestWithContext(ctx, in.Method, u.String(), nil)
	if err != nil {
		return APIResponse{}, err
	}
	for k, v := range in.Headers {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "proxy-authorization" || lk == "cookie" || lk == "host" || strings.Contains(lk, "api-key") {
			continue
		}
		req.Header.Set(k, v)
	}
	hint := c.AuthHint
	if in.AuthHint != nil {
		hint = *in.AuthHint
	}
	if err := hint.Validate(); err != nil {
		return APIResponse{}, &Error{Code: CodeRequestInvalid, Message: err.Error()}
	}
	switch hint.Type {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	case AuthHeader:
		req.Header.Set(hint.Name, c.Secret)
	case AuthQuery:
		q := req.URL.Query()
		q.Set(hint.Name, c.Secret)
		req.URL.RawQuery = q.Encode()
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return APIResponse{}, &Error{Code: CodeUpstreamFailed, Message: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return APIResponse{}, err
	}
	if len(body) > maxBodyBytes {
		return APIResponse{}, &Error{Code: CodeBodyTooLarge, Message: "response too large"}
	}
	body, _ = RedactResponse(body, resp.Header.Get("Content-Type"), []string{c.Secret})
	out := APIResponse{Status: resp.StatusCode, Headers: map[string]string{}, ContentType: resp.Header.Get("Content-Type")}
	if strings.Contains(strings.ToLower(out.ContentType), "json") {
		out.JSONBody = body
	} else {
		s := string(body)
		out.TextBody = &s
	}
	return out, nil
}
