// Package objectid is a thin shim providing the mgo.v2/bson.ObjectId API
// surface we depend on, backed by go.mongodb.org/mongo-driver/v2/bson.ObjectID.
//
// The underlying 12-byte layout and hex encoding are identical to mgo.v2's,
// so existing Bolt databases remain readable.
package objectid

import (
	"encoding/binary"
	"time"

	mgobson "go.mongodb.org/mongo-driver/v2/bson"
)

// ObjectId is a 12-byte MongoDB-compatible identifier whose first 4 bytes
// encode a Unix timestamp, making it time-sortable.
type ObjectId = mgobson.ObjectID

// NewObjectId returns a freshly generated ObjectId timestamped with now.
func NewObjectId() ObjectId {
	return mgobson.NewObjectID()
}

// NewObjectIdWithTime returns an ObjectId whose time prefix encodes t and
// whose remaining bytes are zero. This matches mgo.v2 semantics and is useful
// for building range-scan cursors — mongo-driver's NewObjectIDFromTimestamp
// fills the trailing bytes with process/counter data, which is not what we
// want for bounds.
func NewObjectIdWithTime(t time.Time) ObjectId {
	var b [12]byte
	binary.BigEndian.PutUint32(b[:4], uint32(t.Unix()))
	return ObjectId(b)
}

// IsObjectIdHex reports whether s is a valid 24-char hex-encoded ObjectId.
func IsObjectIdHex(s string) bool {
	if len(s) != 24 {
		return false
	}
	_, err := mgobson.ObjectIDFromHex(s)
	return err == nil
}

// ObjectIdHex parses s as a hex-encoded ObjectId and panics on invalid input,
// matching mgo.v2's panic-on-invalid contract. Callers should guard with
// IsObjectIdHex where the input is untrusted.
func ObjectIdHex(s string) ObjectId {
	id, err := mgobson.ObjectIDFromHex(s)
	if err != nil {
		panic(err)
	}
	return id
}
