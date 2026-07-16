package toolkeys

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

type AuthType string

const (
	AuthBearer AuthType = "bearer"
	AuthHeader AuthType = "header"
	AuthQuery  AuthType = "query"
)

type AuthHint struct {
	Type AuthType `json:"type"`
	Name string   `json:"name,omitempty"`
}

func (h AuthHint) Validate() error {
	switch h.Type {
	case AuthBearer:
		if h.Name != "" {
			return fmt.Errorf("bearer auth cannot name a header")
		}
		return nil
	case AuthHeader, AuthQuery:
		if !httpguts.ValidHeaderFieldName(h.Name) {
			return fmt.Errorf("invalid auth name")
		}
		if h.Type == AuthHeader {
			switch strings.ToLower(h.Name) {
			case "host", "authorization", "proxy-authorization", "cookie", "set-cookie":
				return fmt.Errorf("reserved auth header")
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported auth type")
	}
}

type Credential struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Origin     string    `json:"origin"`
	Secret     string    `json:"-"`
	AuthHint   AuthHint  `json:"auth_hint"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	RotatedAt  time.Time `json:"rotated_at,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

type CredentialMeta struct {
	ID, Label, Origin                string
	AuthHint                         AuthHint
	Enabled                          bool
	CreatedAt, RotatedAt, LastUsedAt time.Time
}

type PendingRequest struct {
	ID, Origin, Reason, DocsURL string
	AuthHint                    AuthHint
	CreatedAt, ExpiresAt        time.Time
}

type PendingRequestInput struct {
	Origin   string   `json:"origin"`
	AuthHint AuthHint `json:"auth_hint"`
	Reason   string   `json:"reason"`
	DocsURL  string   `json:"docs_url,omitempty"`
}

type APIRequest struct {
	CredentialID string
	Method, Path string
	Query        url.Values
	Headers      map[string]string
	JSONBody     []byte
	TextBody     *string
	AuthHint     *AuthHint
}

type APIResponse struct {
	Status      int
	Headers     map[string]string
	ContentType string
	JSONBody    []byte
	TextBody    *string
}

type Code string

const (
	CodeCredentialRequired  Code = "CREDENTIAL_REQUIRED"
	CodeUserActionRequired  Code = "USER_ACTION_REQUIRED"
	CodeBrokerUnavailable   Code = "BROKER_UNAVAILABLE"
	CodeOriginInvalid       Code = "ORIGIN_INVALID"
	CodeRequestInvalid      Code = "REQUEST_INVALID"
	CodeCredentialDisabled  Code = "CREDENTIAL_DISABLED"
	CodeRedirectNotFollowed Code = "REDIRECT_NOT_FOLLOWED"
	CodeBodyTooLarge        Code = "BODY_TOO_LARGE"
	CodeUpstreamFailed      Code = "UPSTREAM_FAILED"
)

type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return e.Message }
