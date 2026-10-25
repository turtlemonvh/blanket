package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/server"
)

// The /auth/ bootstrap API (turtlemonvh/blanket#101); see
// server/serve_auth.go.

// AuthStatus reports whether auth is on (an open route).
func AuthStatus(port int) (server.AuthStatus, error) {
	var out server.AuthStatus
	res, err := doRequest(context.Background(), "GET", fmt.Sprintf("http://localhost:%d/auth/status", port), nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(res.Body, &out)
	return out, err
}

// AuthInit trades the setup code for the first admin token.
func AuthInit(code string, port int) (server.AuthInitResponse, error) {
	var out server.AuthInitResponse
	bts, err := json.Marshal(server.AuthInitRequest{Code: code})
	if err != nil {
		return out, err
	}
	res, err := doRequest(context.Background(), "POST", fmt.Sprintf("http://localhost:%d/auth/init", port), bts, httpx.DefaultRequestTimeout)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(res.Body, &out)
	return out, err
}

// AuthDisable revokes every token that keeps auth on; returns how many.
func AuthDisable(port int) (int, error) {
	var out struct {
		Revoked int `json:"revoked"`
	}
	res, err := doRequest(context.Background(), "POST", fmt.Sprintf("http://localhost:%d/auth/disable", port), nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return 0, err
	}
	err = json.Unmarshal(res.Body, &out)
	return out.Revoked, err
}
