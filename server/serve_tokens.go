package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/auth"
)

// The /token/ API (turtlemonvh/blanket#101). `blanket token create|list|
// revoke` is a thin client over it, using the local admin credential.
// Every route here is admin-only and needs a real token even while auth is
// off (see routeRules), so no network caller can mint the first token.

// TokenCreateRequest is POST /token/'s body.
type TokenCreateRequest struct {
	Name string `json:"name"`
	// Scopes holds scope and preset names, e.g. ["agent"] or
	// ["read", "tasks:manage"].
	Scopes []string `json:"scopes"`
	Types  []string `json:"types,omitempty"`
	Tasks  []string `json:"tasks,omitempty"`
	// Expires is a duration from now ("90d", "12h"); empty never expires.
	Expires string `json:"expires,omitempty"`
}

// TokenCreateResponse is the stored row plus the plaintext, which is
// returned this once and never again.
type TokenCreateResponse struct {
	auth.Token
	Secret string `json:"token"`
}

// ParseExpiry accepts a Go duration or a whole number of days ("90d").
func ParseExpiry(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid expiry %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid expiry %q: use a duration like 12h or a day count like 90d", s)
	}
	return d, nil
}

func (s *ServerConfig) listTokens(c *gin.Context) {
	ts, err := s.Tokens.ListTokens()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ts)
}

// issueToken generates, stores and returns a new token, and refreshes the
// cached auth state. POST /auth/init mints the first admin token with it.
func (s *ServerConfig) issueToken(t auth.Token) (TokenCreateResponse, error) {
	secret, t, err := auth.IssueToken(s.Tokens, t)
	if err != nil {
		return TokenCreateResponse{}, err
	}
	s.refreshAuthEnabled()
	log.WithFields(log.Fields{"tokenId": t.Id.Hex(), "name": t.Name, "scopes": t.Scopes}).Warn("API token created")
	return TokenCreateResponse{Token: t, Secret: secret}, nil
}

func (s *ServerConfig) createToken(c *gin.Context) {
	var req TokenCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	scopes, err := auth.ParseScopes(req.Scopes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, sc := range scopes {
		if sc == auth.ScopeWorker {
			// A worker token must be bound to one worker id, which is
			// phase 2's machinery; an unbound one could act as any worker.
			c.JSON(http.StatusBadRequest, gin.H{"error": "worker tokens are minted by the server for each worker it launches, not by hand"})
			return
		}
	}
	t := auth.Token{
		Name:         req.Name,
		Owner:        principalFrom(c).Token.Owner,
		Scopes:       scopes,
		Restrictions: auth.Restrictions{Types: req.Types, Tasks: req.Tasks},
	}
	if req.Expires != "" {
		d, err := ParseExpiry(req.Expires)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		t.ExpiresTs = time.Now().Add(d).Unix()
	}
	if err := t.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	resp, err := s.issueToken(t)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, resp)
}

func (s *ServerConfig) revokeToken(c *gin.Context) {
	id, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ts, err := s.Tokens.ListTokens()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var target *auth.Token
	admins := 0
	for i := range ts {
		if ts[i].HasScope(auth.ScopeAdmin) {
			admins++
		}
		if ts[i].Id == id {
			target = &ts[i]
		}
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no token with id " + id.Hex()})
		return
	}
	// Revoking the last admin token would leave auth on with nobody able
	// to manage it, or switch it off as a side effect. Either is a
	// surprise; turning auth off has its own command.
	if target.HasScope(auth.ScopeAdmin) && admins == 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "this is the last admin token; use `blanket auth disable` to turn auth off instead"})
		return
	}
	if err := s.Tokens.DeleteToken(id); err != nil {
		c.JSON(statusForTokenError(err), gin.H{"error": err.Error()})
		return
	}
	s.refreshAuthEnabled()
	log.WithFields(log.Fields{"tokenId": id.Hex(), "name": target.Name}).Warn("API token revoked")
	c.JSON(http.StatusOK, gin.H{"id": id.Hex()})
}

func statusForTokenError(err error) int {
	if err == auth.ErrTokenNotFound {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
