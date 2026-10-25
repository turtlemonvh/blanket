package bolt

/*

The `tokens` bucket (turtlemonvh/blanket#101, docs/design/auth.md A.4).

Keyed by auth.HashToken(plaintext); the value is the auth.Token row as
JSON. The plaintext is never written anywhere by the server except the
local credentials file `blanket auth init` asks for.

The bucket is created by migration v2 (migrations.go). The accessors below
still tolerate its absence -- reads see an empty bucket, writes create it --
because test fixtures build databases through NewBlanketBoltDB, which runs
no migrations.

*/

import (
	"encoding/json"
	"fmt"

	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/objectid"
	bolt "go.etcd.io/bbolt"
)

// BOLTDB_TOKEN_BUCKET holds API tokens.
const BOLTDB_TOKEN_BUCKET = "tokens"

// Meta keys for auth (A.4): the pending setup code's hash, and when auth
// was switched on.
const (
	MetaKeySetupCodeHash = "authSetupCodeHash"
	MetaKeyAuthEnabledTs = "authEnabledTs"
)

var (
	_ auth.Store      = (*BlanketBoltDB)(nil)
	_ auth.SetupStore = (*BlanketBoltDB)(nil)
)

func (DB *BlanketBoltDB) SetSetupCodeHash(hash string) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		if hash == "" {
			return deleteMeta(tx, MetaKeySetupCodeHash)
		}
		return putMetaJSON(tx, MetaKeySetupCodeHash, hash)
	})
}

func (DB *BlanketBoltDB) SetupCodeHash() (string, error) {
	var h string
	err := DB.db.View(func(tx *bolt.Tx) error {
		_, err := getMetaJSON(tx, MetaKeySetupCodeHash, &h)
		return err
	})
	return h, err
}

func (DB *BlanketBoltDB) SetAuthEnabledTs(ts int64) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		if ts == 0 {
			return deleteMeta(tx, MetaKeyAuthEnabledTs)
		}
		return putMetaJSON(tx, MetaKeyAuthEnabledTs, ts)
	})
}

func forEachToken(b *bolt.Bucket, fn func(hash []byte, t auth.Token) error) error {
	if b == nil {
		return nil
	}
	return b.ForEach(func(k, v []byte) error {
		var t auth.Token
		if err := json.Unmarshal(v, &t); err != nil {
			return fmt.Errorf("token row is undecodable: %w", err)
		}
		return fn(k, t)
	})
}

func (DB *BlanketBoltDB) CreateToken(t auth.Token, hash string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	bts, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return DB.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(BOLTDB_TOKEN_BUCKET))
		if err != nil {
			return err
		}
		if b.Get([]byte(hash)) != nil {
			return fmt.Errorf("a token with this hash already exists")
		}
		return b.Put([]byte(hash), bts)
	})
}

func (DB *BlanketBoltDB) TokenByHash(hash string) (auth.Token, error) {
	var t auth.Token
	err := DB.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_TOKEN_BUCKET))
		if b == nil {
			return auth.ErrTokenNotFound
		}
		v := b.Get([]byte(hash))
		if v == nil {
			return auth.ErrTokenNotFound
		}
		return json.Unmarshal(v, &t)
	})
	return t, err
}

func (DB *BlanketBoltDB) ListTokens() ([]auth.Token, error) {
	out := []auth.Token{}
	err := DB.db.View(func(tx *bolt.Tx) error {
		return forEachToken(tx.Bucket([]byte(BOLTDB_TOKEN_BUCKET)), func(_ []byte, t auth.Token) error {
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

func (DB *BlanketBoltDB) DeleteToken(id objectid.ObjectId) error {
	n, err := DB.DeleteTokensWhere(func(t auth.Token) bool { return t.Id == id })
	if err == nil && n == 0 {
		return auth.ErrTokenNotFound
	}
	return err
}

func (DB *BlanketBoltDB) DeleteTokensWhere(match func(auth.Token) bool) (int, error) {
	n := 0
	err := DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_TOKEN_BUCKET))
		var doomed [][]byte
		if err := forEachToken(b, func(k []byte, t auth.Token) error {
			if match(t) {
				// ForEach's key slice is only valid inside the
				// transaction and must not be deleted mid-iteration.
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		n = len(doomed)
		return nil
	})
	return n, err
}

func (DB *BlanketBoltDB) TouchToken(hash string, ts int64) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_TOKEN_BUCKET))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(hash))
		if v == nil {
			return nil
		}
		var t auth.Token
		if err := json.Unmarshal(v, &t); err != nil {
			return err
		}
		if ts <= t.LastUsedTs {
			return nil
		}
		t.LastUsedTs = ts
		bts, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return b.Put([]byte(hash), bts)
	})
}

// migrationAddTokensBucket is schema v2: the tokens bucket. Additive -- an
// older reader would ignore the bucket -- but it still bumps the version,
// and that is the point: a blanket from before auth existed must refuse to
// open a database that has tokens in it, rather than serve everything
// unauthenticated because it doesn't know to look.
var migrationAddTokensBucket = Migration{
	Version: 2,
	Name:    "add tokens bucket (auth phase 1, turtlemonvh/blanket#101)",
	Up: func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(BOLTDB_TOKEN_BUCKET))
		return err
	},
}
