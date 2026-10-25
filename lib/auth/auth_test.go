package auth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

func TestParseScopesExpandsPresetsAndDedupes(t *testing.T) {
	got, err := ParseScopes([]string{"agent", "read,tasks:manage"})
	require.NoError(t, err)
	assert.Equal(t, []Scope{ScopeRead, ScopeSubmit, ScopeTasksManage}, got)

	_, err = ParseScopes([]string{"raed"})
	assert.ErrorContains(t, err, "unknown scope or preset")

	_, err = ParseScopes([]string{" , "})
	assert.Error(t, err, "no scopes at all")
}

func TestPresetsMatchTheBrief(t *testing.T) {
	assert.Equal(t, []Scope{ScopeRead}, Presets["viewer"])
	assert.Equal(t, []Scope{ScopeRead, ScopeSubmit}, Presets["agent"])
	assert.ElementsMatch(t, []Scope{ScopeRead, ScopeSubmit, ScopeTasksManage, ScopeTypesWrite, ScopeWorkersManage}, Presets["operator"])
	assert.Equal(t, []Scope{ScopeAdmin}, Presets["admin"])
}

func TestTokenStringShapeAndHash(t *testing.T) {
	a, err := NewTokenString()
	require.NoError(t, err)
	b, err := NewTokenString()
	require.NoError(t, err)

	assert.NotEqual(t, a, b)
	assert.True(t, WellFormed(a), a)
	assert.Len(t, a, len(TokenPrefix)+52, "32 bytes is 52 unpadded base32 characters")
	assert.False(t, WellFormed("blk_short"))
	assert.False(t, WellFormed("xyz_"+a[4:]))

	assert.Len(t, HashToken(a), 64)
	assert.Equal(t, HashToken(a), HashToken(a))
	assert.NotEqual(t, HashToken(a), HashToken(b))
}

func TestEnablesAuth(t *testing.T) {
	assert.False(t, Token{Scopes: []Scope{ScopeWorker}}.EnablesAuth(), "worker tokens never switch auth on")
	assert.True(t, Token{Scopes: []Scope{ScopeRead}}.EnablesAuth())
	assert.True(t, Token{Scopes: []Scope{ScopeAdmin}}.EnablesAuth())
}

func TestValidate(t *testing.T) {
	ok := Token{Name: "n", Scopes: []Scope{ScopeRead}}
	assert.NoError(t, ok.Validate())

	assert.Error(t, Token{Scopes: []Scope{ScopeRead}}.Validate(), "name required")
	assert.Error(t, Token{Name: "n"}.Validate(), "scope required")
	assert.Error(t, Token{Name: "n", Scopes: []Scope{"bogus"}}.Validate())
	assert.Error(t, Token{Name: "n", Scopes: []Scope{ScopeAdmin}, Restrictions: Restrictions{Types: []string{"x"}}}.Validate(),
		"a restricted admin is refused")
	assert.Error(t, Token{Name: "n", Scopes: []Scope{ScopeRead}, Restrictions: Restrictions{Tasks: []string{"nope"}}}.Validate())
}

func TestExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	assert.False(t, Token{}.Expired(now), "zero means never")
	assert.False(t, Token{ExpiresTs: 1001}.Expired(now))
	assert.True(t, Token{ExpiresTs: 1000}.Expired(now))
}

func TestAuthOffPrincipalMayDoAnything(t *testing.T) {
	p := AuthOffPrincipal()
	assert.True(t, p.AuthOff())
	for _, s := range AllScopes {
		assert.True(t, p.Has(s))
	}
	assert.True(t, p.CanSubmitType("anything"))
	assert.True(t, p.CanReadTask(TaskRef{Type: "x"}))
	assert.Equal(t, "", p.Id())
}

func TestAdminHasEverything(t *testing.T) {
	p := NewPrincipal(Token{Id: objectid.NewObjectId(), Scopes: []Scope{ScopeAdmin}})
	for _, s := range AllScopes {
		assert.True(t, p.Has(s), s)
	}
}

func TestTypeRestrictedAgent(t *testing.T) {
	p := NewPrincipal(Token{
		Id:           objectid.NewObjectId(),
		Scopes:       []Scope{ScopeRead, ScopeSubmit},
		Restrictions: Restrictions{Types: []string{"transcribe"}},
	})
	assert.True(t, p.CanSubmitType("transcribe"))
	assert.False(t, p.CanSubmitType("deploy"))
	assert.True(t, p.CanReadTask(TaskRef{Id: "a", Type: "transcribe"}))
	assert.False(t, p.CanReadTask(TaskRef{Id: "b", Type: "deploy"}))
	assert.False(t, p.Has(ScopeTasksManage))
}

func TestTaskGrantPlusOwnTasks(t *testing.T) {
	id := objectid.NewObjectId()
	granted := objectid.NewObjectId().Hex()
	p := NewPrincipal(Token{
		Id:           id,
		Scopes:       []Scope{ScopeRead, ScopeSubmit},
		Restrictions: Restrictions{Tasks: []string{granted}},
	})
	assert.True(t, p.CanReadTask(TaskRef{Id: granted, Type: "x"}))
	assert.False(t, p.CanReadTask(TaskRef{Id: objectid.NewObjectId().Hex(), Type: "x"}))
	// The one addition restrictions allow: tasks it created.
	own := TaskRef{Id: objectid.NewObjectId().Hex(), Type: "x", CreatedBy: id.Hex()}
	assert.True(t, p.CanReadTask(own))
	assert.True(t, p.CanCancelTask(own), "an agent can stop its own mistake")
	assert.False(t, p.CanManageTask(own), "...but cancel is all it gets without tasks:manage")
	assert.False(t, p.CanCancelTask(TaskRef{Id: granted, Type: "x"}), "read on a task is not cancel")
}

func TestSubmitOnlyTokenStillReadsItsOwnTasks(t *testing.T) {
	id := objectid.NewObjectId()
	p := NewPrincipal(Token{Id: id, Scopes: []Scope{ScopeSubmit}})
	assert.True(t, p.CanReadTask(TaskRef{CreatedBy: id.Hex()}))
	assert.False(t, p.CanReadTask(TaskRef{CreatedBy: objectid.NewObjectId().Hex()}))
	assert.False(t, p.CanReadTask(TaskRef{}), "an empty createdBy is nobody's")
}

func TestTypeRestrictedManagerOnlyManagesItsTypes(t *testing.T) {
	p := NewPrincipal(Token{
		Id:           objectid.NewObjectId(),
		Scopes:       []Scope{ScopeTasksManage},
		Restrictions: Restrictions{Types: []string{"a"}},
	})
	assert.True(t, p.CanManageTask(TaskRef{Type: "a"}))
	assert.False(t, p.CanManageTask(TaskRef{Type: "b"}))
}

func TestTokenSourcePrefersExplicitThenRereadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), CredentialsFileName)

	assert.Equal(t, "blk_explicit", NewTokenSource(" blk_explicit ", path).Token())

	src := NewTokenSource("", path)
	assert.Equal(t, "", src.Token(), "no file yet")

	require.NoError(t, os.WriteFile(path, []byte("blk_one\n"), 0600))
	assert.Equal(t, "blk_one", src.Token())

	// A credential written later (auth init after a worker started) is
	// picked up without restarting the reader.
	require.NoError(t, os.WriteFile(path, []byte("blk_second\n"), 0600))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
	assert.Equal(t, "blk_second", src.Token())

	require.NoError(t, os.Remove(path))
	assert.Equal(t, "", src.Token(), "auth disable removed it")
}

func TestDataDirIsTheDatabaseDirectory(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, dir, DataDir(filepath.Join(dir, "blanket.db")))
	assert.Equal(t, filepath.Join(dir, "credentials"), CredentialsPath(dir))
}

type touchRecorder struct {
	Store
	mu      sync.Mutex
	touches int
}

func (r *touchRecorder) TouchToken(string, int64) error {
	r.mu.Lock()
	r.touches++
	r.mu.Unlock()
	return nil
}

func TestLastUsedBatcherWritesAtMostOncePerInterval(t *testing.T) {
	rec := &touchRecorder{}
	b := NewLastUsedBatcher(rec)
	now := time.Unix(10000, 0)
	b.now = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		b.Touch("h1")
	}
	b.Touch("h2")
	b.Wait()
	assert.Equal(t, 2, rec.touches, "one write per token, not per request")

	now = now.Add(LastUsedInterval)
	b.Touch("h1")
	b.Wait()
	assert.Equal(t, 3, rec.touches, "a minute later it writes again")
}
