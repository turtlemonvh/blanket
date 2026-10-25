package server

/*

Bootstrap: the setup code, POST /auth/init and POST /auth/disable
(turtlemonvh/blanket#101, docs/design/auth.md row 4 and A.5).

While auth is off the server generates a one-time setup code at startup,
logs it, writes it to <dataDir>/setup-code (0600) and keeps only its hash
in the meta bucket. The code is the proof of being the operator: seeing the
server's log or reading a file beside its database. A tailnet peer has
neither, so although POST /auth/init is reachable by anyone, nobody without
the code can claim admin -- not even while auth is off.

*/

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/auth"
)

// setupFailureDelay is how long a wrong setup code waits before its 403.
// A var so tests can shorten it.
var setupFailureDelay = defaultSetupFailureDelay

const defaultSetupFailureDelay = time.Second

// setupMu serializes the check-and-issue part of POST /auth/init, so two
// requests carrying the right code can't both mint an admin token.
//
// The failure delay is served *after* releasing it. Sleeping under the lock
// let a flood of wrong guesses queue the legitimate operator behind a
// second per guess (turtlemonvh/blanket#101 review, L6). The cost is that
// the delay now slows a guesser that waits for each answer, not one that
// sends many requests in parallel: any global limit on checks would hand
// that same flood a way to lock the operator out again. What bounds a
// parallel guesser is the 40-bit code and the server's request rate, and
// the code exists only while auth is off, when every other route is open
// anyway.
var setupMu sync.Mutex

// AuthInitRequest is POST /auth/init's body.
type AuthInitRequest struct {
	Code string `json:"code"`
}

// AuthInitResponse is the admin token (shown once) plus where the server
// wrote it for local CLIs.
type AuthInitResponse struct {
	TokenCreateResponse
	CredentialsPath string `json:"credentialsPath,omitempty"`
}

// AuthStatus is GET /auth/status's body.
type AuthStatus struct {
	Enabled      bool `json:"enabled"`
	SetupPending bool `json:"setupPending"`
}

// startSetupIfAuthOff issues a fresh setup code when auth is off. Called at
// startup and after `auth disable`.
func (s *ServerConfig) startSetupIfAuthOff() {
	if s.Tokens == nil || s.AuthEnabled() {
		return
	}
	log.Warn("authentication is off: anyone who can reach this port can run tasks on this machine. Run `blanket auth init` on this host to turn it on (docs/usage.md, \"Authentication\")")
	if s.DataDir == "" {
		return
	}
	code, err := auth.NewSetupCode()
	if err == nil {
		err = s.Tokens.SetSetupCodeHash(auth.HashSetupCode(code))
	}
	if err == nil {
		err = auth.WriteSecretFile(auth.SetupCodePath(s.DataDir), code+"\n")
	}
	if err != nil {
		log.WithField("err", err).Error("could not issue an auth setup code; `blanket auth init` will not work until the server restarts")
		return
	}
	log.WithField("file", auth.SetupCodePath(s.DataDir)).Warn("auth setup code: " + code)
}

func (s *ServerConfig) authStatus(c *gin.Context) {
	st := AuthStatus{Enabled: s.AuthEnabled()}
	if s.Tokens != nil && !st.Enabled {
		h, _ := s.Tokens.SetupCodeHash()
		st.SetupPending = h != ""
	}
	c.JSON(http.StatusOK, st)
}

func (s *ServerConfig) authInit(c *gin.Context) {
	var req AuthInitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if s.Tokens == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "this server has no token store"})
		return
	}

	resp, status, err := s.redeemSetupCode(req.Code)
	if status == http.StatusForbidden {
		// Outside setupMu; see its comment.
		time.Sleep(setupFailureDelay)
		log.WithField("remote", c.Request.RemoteAddr).Warn("refused POST /auth/init: wrong or missing setup code")
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid setup code; it is in the server log and in the setup-code file beside the database"})
		return
	}
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	out := AuthInitResponse{TokenCreateResponse: resp}
	if s.DataDir != "" {
		auth.RemoveIfExists(auth.SetupCodePath(s.DataDir))
		path := auth.CredentialsPath(s.DataDir)
		if err := auth.WriteSecretFile(path, resp.Secret+"\n"); err != nil {
			// The token is still returned, so the caller can store it
			// by hand; local CLIs just won't find it on their own.
			log.WithField("err", err).Error("auth is on, but the admin credentials file could not be written")
		} else {
			out.CredentialsPath = path
		}
	}
	log.WithField("tokenId", resp.Id.Hex()).Warn("authentication is now on")
	c.JSON(http.StatusCreated, out)
}

// redeemSetupCode checks code and, if it is right, issues the first admin
// token and spends the code, all under setupMu. A wrong or missing code is
// 403 with a nil error; the caller serves the failure delay after this
// returns, so the lock is never held across it.
func (s *ServerConfig) redeemSetupCode(code string) (TokenCreateResponse, int, error) {
	setupMu.Lock()
	defer setupMu.Unlock()

	if s.AuthEnabled() {
		return TokenCreateResponse{}, http.StatusConflict, errors.New("authentication is already on")
	}
	stored, err := s.Tokens.SetupCodeHash()
	if err != nil {
		return TokenCreateResponse{}, http.StatusInternalServerError, err
	}
	given := auth.HashSetupCode(code)
	if stored == "" || code == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(given)) != 1 {
		return TokenCreateResponse{}, http.StatusForbidden, nil
	}

	resp, err := s.issueToken(auth.InitAdminToken())
	if err != nil {
		return TokenCreateResponse{}, http.StatusInternalServerError, err
	}
	// Single use: the code is spent whether or not the rest succeeds.
	if err := s.Tokens.SetSetupCodeHash(""); err != nil {
		log.WithField("err", err).Error("could not clear the spent setup code")
	}
	if err := s.Tokens.SetAuthEnabledTs(time.Now().Unix()); err != nil {
		log.WithField("err", err).Warn("could not record when auth was enabled")
	}
	return resp, http.StatusCreated, nil
}

// disableNeedsLocalProof is the extra check POST /auth/disable makes on
// top of its admin row. The brief keeps turning auth off a local act (row
// 4, docs/usage.md), but an admin row alone let any admin token do it
// from anywhere (turtlemonvh/blanket#101 review, L2). So, mirroring how
// init needs the setup code from a file beside the database, disable needs
// both:
//
//   - a loopback peer, decided from the socket like opsGuard does, and
//   - the bearer token to be the one in <dataDir>/credentials, which is
//     what `blanket auth disable` on the server's host sends. Another
//     admin token, minted with `blanket token create`, is refused.
//
// Behind `tailscale serve` every peer is loopback, so the file token is
// the part that carries the weight: someone holding a copy of it can
// still do this, as they can do everything else an admin can. Returns 0
// when the request passes.
func (s *ServerConfig) disableNeedsLocalProof(c *gin.Context) (int, string) {
	if !isLoopbackAddr(c.Request.RemoteAddr) {
		return http.StatusForbidden, "auth disable is local only: run `blanket auth disable` on the server's host"
	}
	if s.DataDir == "" {
		return http.StatusForbidden, "this server has no credentials file, so auth can only be disabled offline (stop the server, then run `blanket auth disable`)"
	}
	want, err := auth.ReadSecretFile(auth.CredentialsPath(s.DataDir))
	if err != nil || want == "" {
		return http.StatusForbidden, "the credentials file beside the database is missing; stop the server and run `blanket auth disable` to turn auth off offline"
	}
	h := c.GetHeader("Authorization")
	got := ""
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		got = strings.TrimSpace(h[7:])
	}
	if subtle.ConstantTimeCompare([]byte(auth.HashToken(got)), []byte(auth.HashToken(want))) != 1 {
		return http.StatusForbidden, "auth disable needs the token from the credentials file beside the database: run `blanket auth disable` on the server's host, without --token or BLANKET_TOKEN"
	}
	return 0, ""
}

// authDisable turns auth off by revoking every token that keeps it on
// (row 4). Worker tokens survive, as they never turned it on.
func (s *ServerConfig) authDisable(c *gin.Context) {
	if status, msg := s.disableNeedsLocalProof(c); status != 0 {
		log.WithField("remote", c.Request.RemoteAddr).Warn("refused POST /auth/disable: " + msg)
		c.JSON(status, gin.H{"error": msg})
		return
	}
	n, err := s.Tokens.DeleteTokensWhere(func(t auth.Token) bool { return t.EnablesAuth() })
	s.refreshAuthEnabled()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := s.Tokens.SetAuthEnabledTs(0); err != nil {
		log.WithField("err", err).Warn("could not clear authEnabledTs")
	}
	if s.DataDir != "" {
		if err := auth.RemoveIfExists(auth.CredentialsPath(s.DataDir)); err != nil {
			log.WithField("err", err).Warn("could not remove the credentials file")
		}
	}
	log.WithField("revoked", n).Warn("authentication turned off")
	s.startSetupIfAuthOff()
	c.JSON(http.StatusOK, gin.H{"revoked": n})
}
