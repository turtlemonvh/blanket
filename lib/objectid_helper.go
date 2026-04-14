package lib

import (
	"github.com/turtlemonvh/blanket/lib/objectid"
)

// Gets the full byte representation of the objectid
// Errors are ignored because just casting a string object to a byte slice will never result in an error
func IdBytes(id objectid.ObjectId) []byte {
	bts, _ := id.MarshalJSON()
	return bts
}
