package server

/*

The ops surface: privileged, local-only endpoints (turtlemonvh/blanket#23
phase 4; decision row 6 of the design brief).

`POST /ops/backup` is the first of these. Phase 5's `/ops/restart*` is the
reason the guard below is a reusable middleware rather than four lines
inlined into one handler — a security control that has to be remembered
and re-typed at each new call site is one that will eventually be
forgotten at one of them.

## The threat this closes

blanket has no authentication of any kind, and its CORS policy is
`AllowedOrigins: ["*"]` covering POST (server.go). On a machine running
blanket, any web page the user visits can therefore make cross-origin
requests to http://localhost:8773 and read the responses. That is a
tolerable posture for endpoints that list tasks on a single-user box. It is
not tolerable for endpoints that write files or restart the server.

Three layers, each of which alone would be insufficient:

 1. **Loopback only**, decided from the socket's own RemoteAddr — never
    from ClientIP(). gin's ClientIP consults X-Forwarded-For, which is a
    header the caller writes, so a remote attacker could simply claim to
    be 127.0.0.1. RemoteAddr is the kernel's view and cannot be spoofed
    over TCP.

 2. **A required `X-Blanket-Restart` header.** This is not a secret and is
    not treated as one; its job is to force the browser into a CORS
    *preflight*. A cross-origin POST with only "simple" headers is sent
    without one — the browser blocks the attacker from *reading* the
    response, but the request still lands. A custom header makes the
    browser ask permission first, which is a request our own server gets
    to refuse.

 3. **Carved out of the wildcard CORS handler** (see GetRouter), so that
    preflight is refused. Without this the permissive handler would happily
    approve the preflight it exists to trigger, and layer 2 would be
    decoration.

Non-browser callers — curl, the CLI, a script — are unaffected: they set
the header and connect from loopback, which is what a local operator is.

There is deliberately no token yet (brief decision row 6). Loopback-only
covers the single-machine install, which is the only shape that exists
today; `ops.token` is additive when someone needs to drive an upgrade
from off-box.

*/

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const (
	// OpsHeader is the header every /ops/ mutating request must carry. Its
	// value is not checked: presence is the whole point (see layer 2
	// above). Named for the restart endpoints because those are the ones
	// an operator will type by hand.
	OpsHeader = "X-Blanket-Restart"

	// OpsPathPrefix is the prefix carved out of the wildcard CORS handler.
	OpsPathPrefix = "/ops/"
)

// isLoopbackAddr reports whether a `host:port` string as found in
// http.Request.RemoteAddr is a loopback address.
//
// A malformed RemoteAddr answers false. That errs toward refusing, which
// is the right direction for a guard: an ops endpoint that fails closed
// costs an operator one confusing error, while one that fails open costs
// them their install.
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// Some transports (a unix socket, an httptest recorder with no
		// address set) have no port. Try the raw value.
		host = remoteAddr
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// opsGuard is the middleware described in the file header. Apply it to
// every mutating /ops/ route.
func opsGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isLoopbackAddr(c.Request.RemoteAddr) {
			log.WithFields(log.Fields{
				"path":   c.Request.URL.Path,
				"remote": c.Request.RemoteAddr,
			}).Warn("refused a non-loopback request to an ops endpoint")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "ops endpoints are reachable from loopback only",
			})
			return
		}
		if c.GetHeader(OpsHeader) == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "missing required header " + OpsHeader,
			})
			return
		}
		c.Next()
	}
}

// opsBackup handles POST /ops/backup: take a database backup now, while
// the server keeps serving.
//
// It works *because* the server is running rather than in spite of it —
// bolt's MVCC gives a read transaction a consistent point-in-time image,
// so no pause and no lock handoff is needed. This is the only way to back
// up a live install: the CLI cannot open the database while the server
// holds its exclusive lock.
//
// The optional `dir` query parameter overrides where the backup lands;
// with it unset the server uses its configured backup directory.
func (s *ServerConfig) opsBackup(c *gin.Context) {
	dir := c.Query("dir")
	if dir == "" {
		dir = s.BackupDir
	}

	path, err := s.DB.Backup(dir)
	if err != nil {
		log.WithField("err", err).Error("backup failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	version, err := s.DB.SchemaVersion()
	if err != nil {
		// The backup itself succeeded; not being able to restate the
		// version is not a reason to report failure.
		log.WithField("err", err).Warn("backup written but the schema version could not be read back")
	}

	c.JSON(http.StatusOK, gin.H{
		"path":          path,
		"schemaVersion": version,
	})
}
