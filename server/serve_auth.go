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
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/auth"
)

// setupFailureDelay is how long a wrong setup code holds the (single,
// serialized) init path. One guess a second against a 40-bit code. A var
// so tests can shorten it.
var setupFailureDelay = defaultSetupFailureDelay

const defaultSetupFailureDelay = time.Second

// setupMu serializes POST /auth/init, so the delay above applies to every
// attempt in turn rather than to each of many parallel ones.
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

	setupMu.Lock()
	defer setupMu.Unlock()

	if s.AuthEnabled() {
		c.JSON(http.StatusConflict, gin.H{"error": "authentication is already on"})
		return
	}
	stored, err := s.Tokens.SetupCodeHash()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	given := auth.HashSetupCode(req.Code)
	if stored == "" || req.Code == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(given)) != 1 {
		time.Sleep(setupFailureDelay)
		log.WithField("remote", c.Request.RemoteAddr).Warn("refused POST /auth/init: wrong or missing setup code")
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid setup code; it is in the server log and in the setup-code file beside the database"})
		return
	}

	resp, err := s.issueToken(auth.InitAdminToken())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Single use: the code is spent whether or not the rest succeeds.
	if err := s.Tokens.SetSetupCodeHash(""); err != nil {
		log.WithField("err", err).Error("could not clear the spent setup code")
	}
	if err := s.Tokens.SetAuthEnabledTs(time.Now().Unix()); err != nil {
		log.WithField("err", err).Warn("could not record when auth was enabled")
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

// authDisable turns auth off by revoking every token that keeps it on
// (row 4). Worker tokens survive, as they never turned it on.
func (s *ServerConfig) authDisable(c *gin.Context) {
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
