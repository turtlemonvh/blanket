package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/turtlemonvh/blanket/lib/objectid"
)

// TokenPrefix marks a blanket token, so one pasted into the wrong place is
// recognizable (and greppable by secret scanners).
const TokenPrefix = "blk_"

// tokenRandomBytes is how much randomness a token carries (A.1).
const tokenRandomBytes = 32

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrTokenNotFound is what a Store returns for an unknown hash or id.
var ErrTokenNotFound = errors.New("token not found")

// Restrictions narrow what a token's scopes allow (decision row 2). Empty
// lists mean "no restriction".
type Restrictions struct {
	// Types limits submit to these task types, and read to tasks of them.
	Types []string `json:"types,omitempty"`
	// Tasks limits read to these task ids (the "share one task" case).
	Tasks []string `json:"tasks,omitempty"`
}

// Restricted reports whether either list is set.
func (r Restrictions) Restricted() bool {
	return len(r.Types) > 0 || len(r.Tasks) > 0
}

// Token is one row of the tokens bucket. The plaintext is never stored;
// the bucket is keyed by HashToken(plaintext).
type Token struct {
	Id   objectid.ObjectId `json:"id"`
	Name string            `json:"name"`
	// Owner is who the token acts for: a user id (phase 3), a worker id for
	// worker tokens (phase 2), or a free-form label for tokens minted from
	// the CLI in phase 1 ("admin" for the one `blanket auth init` writes).
	Owner  string  `json:"owner"`
	Scopes []Scope `json:"scopes"`
	Restrictions
	CreatedTs int64 `json:"createdTs"`
	// ExpiresTs is 0 for a token that never expires (decision row 7).
	ExpiresTs int64 `json:"expiresTs,omitempty"`
	// LastUsedTs is written at most once a minute per token; see
	// LastUsedBatcher.
	LastUsedTs int64 `json:"lastUsedTs,omitempty"`
}

// Expired reports whether the token has an expiry and it has passed.
func (t Token) Expired(now time.Time) bool {
	return t.ExpiresTs != 0 && now.Unix() >= t.ExpiresTs
}

// HasScope reports whether s is literally among the token's scopes (no
// admin expansion; see Principal.Has for that).
func (t Token) HasScope(s Scope) bool {
	for _, v := range t.Scopes {
		if v == s {
			return true
		}
	}
	return false
}

// EnablesAuth reports whether this token's existence turns auth on (row 4:
// auth is on iff an admin or a non-worker token exists). A token whose
// only scope is worker does not, so the server minting worker tokens can
// never switch auth on by itself.
func (t Token) EnablesAuth() bool {
	for _, s := range t.Scopes {
		if s != ScopeWorker {
			return true
		}
	}
	return false
}

// Validate checks a token row before it is stored.
func (t Token) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("a token needs a name")
	}
	if len(t.Scopes) == 0 {
		return fmt.Errorf("a token needs at least one scope")
	}
	for _, s := range t.Scopes {
		if !validScope(s) {
			return fmt.Errorf("unknown scope %q", s)
		}
	}
	// admin is "everything"; a restricted admin is a contradiction that
	// would leave /config/ and /ops/* deciding which half wins.
	if t.HasScope(ScopeAdmin) && t.Restricted() {
		return fmt.Errorf("an admin token cannot carry type or task restrictions")
	}
	for _, id := range t.Tasks {
		if !objectid.IsObjectIdHex(id) {
			return fmt.Errorf("%q is not a valid task id", id)
		}
	}
	return nil
}

// NewTokenString returns a fresh plaintext token: TokenPrefix followed by
// 32 bytes from crypto/rand, base32-encoded (lowercase, unpadded).
func NewTokenString() (string, error) {
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPrefix + strings.ToLower(tokenEncoding.EncodeToString(b)), nil
}

// HashToken is the key a token is stored under: hex SHA-256 of the
// plaintext. A token is 256 bits of randomness, so an unsalted fast hash
// is the right tool -- there is nothing to brute-force, and a slow hash
// would only cost every request.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// WellFormed reports whether s has the shape of a blanket token. Used to
// refuse obviously wrong values before touching the store.
func WellFormed(s string) bool {
	if !strings.HasPrefix(s, TokenPrefix) {
		return false
	}
	body := strings.ToUpper(strings.TrimPrefix(s, TokenPrefix))
	b, err := tokenEncoding.DecodeString(body)
	return err == nil && len(b) == tokenRandomBytes
}

// Store is the persistence the server needs for tokens. lib/bolt
// implements it on the same file as everything else.
type Store interface {
	// CreateToken stores t under hash. The hash must not already exist.
	CreateToken(t Token, hash string) error
	// TokenByHash returns ErrTokenNotFound for an unknown hash.
	TokenByHash(hash string) (Token, error)
	// ListTokens returns every token, worker tokens included.
	ListTokens() ([]Token, error)
	// DeleteToken revokes by id; ErrTokenNotFound if there is none.
	DeleteToken(id objectid.ObjectId) error
	// DeleteTokensWhere revokes every token match returns true for, in one
	// transaction, and reports how many it removed.
	DeleteTokensWhere(match func(Token) bool) (int, error)
	// TouchToken records a lastUsed time. A missing token is not an error:
	// it was revoked between the request and the batched write.
	TouchToken(hash string, ts int64) error
}

// AnyEnablesAuth reports whether any of ts turns auth on.
func AnyEnablesAuth(ts []Token) bool {
	for _, t := range ts {
		if t.EnablesAuth() {
			return true
		}
	}
	return false
}
