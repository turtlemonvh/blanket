package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/server"
)

// The /token/ API (turtlemonvh/blanket#101). The caller's own credential
// is attached by lib/httpx; see command/auth_client.go.

// CreateToken mints a token and returns it with its one-time plaintext.
func CreateToken(req server.TokenCreateRequest, port int) (server.TokenCreateResponse, error) {
	var out server.TokenCreateResponse
	bts, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	res, err := doRequest(context.Background(), "POST", fmt.Sprintf("http://localhost:%d/token/", port), bts, httpx.DefaultRequestTimeout)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(res.Body, &out)
	return out, err
}

// ListTokens returns every token's row (never a plaintext).
func ListTokens(port int) ([]auth.Token, error) {
	var out []auth.Token
	res, err := doRequest(context.Background(), "GET", fmt.Sprintf("http://localhost:%d/token/", port), nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(res.Body, &out)
	return out, err
}

// RevokeToken deletes a token by id.
func RevokeToken(id string, port int) error {
	_, err := doRequest(context.Background(), "DELETE", fmt.Sprintf("http://localhost:%d/token/%s", port, id), nil, httpx.DefaultRequestTimeout)
	return err
}
