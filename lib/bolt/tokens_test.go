package bolt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

func newTestTokenStore(t *testing.T) *BlanketBoltDB {
	t.Helper()
	DB, closer := NewTestDB()
	t.Cleanup(closer)
	return DB.(*BlanketBoltDB)
}

func TestTokenStoreRoundTrip(t *testing.T) {
	s := newTestTokenStore(t)

	ts, err := s.ListTokens()
	require.NoError(t, err)
	assert.Empty(t, ts, "a database with no tokens bucket reads as empty")

	_, err = s.TokenByHash("nope")
	assert.ErrorIs(t, err, auth.ErrTokenNotFound)

	tok := auth.Token{Id: objectid.NewObjectId(), Name: "ci", Owner: "admin", Scopes: []auth.Scope{auth.ScopeRead}, CreatedTs: 5}
	require.NoError(t, s.CreateToken(tok, "hash1"))
	assert.Error(t, s.CreateToken(tok, "hash1"), "a hash is unique")

	got, err := s.TokenByHash("hash1")
	require.NoError(t, err)
	assert.Equal(t, tok, got)

	require.NoError(t, s.TouchToken("hash1", 100))
	require.NoError(t, s.TouchToken("hash1", 50), "an older touch is ignored, not an error")
	require.NoError(t, s.TouchToken("gone", 100), "a revoked token's late touch is not an error")
	got, _ = s.TokenByHash("hash1")
	assert.Equal(t, int64(100), got.LastUsedTs)

	assert.ErrorIs(t, s.DeleteToken(objectid.NewObjectId()), auth.ErrTokenNotFound)
	require.NoError(t, s.DeleteToken(tok.Id))
	_, err = s.TokenByHash("hash1")
	assert.ErrorIs(t, err, auth.ErrTokenNotFound)
}

func TestCreateTokenValidates(t *testing.T) {
	s := newTestTokenStore(t)
	assert.Error(t, s.CreateToken(auth.Token{Id: objectid.NewObjectId(), Name: "x"}, "h"), "no scopes")
}

func TestDeleteTokensWhere(t *testing.T) {
	s := newTestTokenStore(t)
	for i, scopes := range [][]auth.Scope{{auth.ScopeAdmin}, {auth.ScopeRead}, {auth.ScopeWorker}} {
		require.NoError(t, s.CreateToken(auth.Token{Id: objectid.NewObjectId(), Name: "t", Scopes: scopes}, string(rune('a'+i))))
	}
	n, err := s.DeleteTokensWhere(func(t auth.Token) bool { return t.EnablesAuth() })
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	left, err := s.ListTokens()
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, []auth.Scope{auth.ScopeWorker}, left[0].Scopes)
	assert.False(t, auth.AnyEnablesAuth(left), "only worker tokens left: auth is off")
}
