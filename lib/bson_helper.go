package lib

import (
	"gopkg.in/mgo.v2/bson"
)

// Gets the full byte representation of the objectid
// Errors are ignored because just casting a string object to a byte slice will never result in an error
func IdBytes(id bson.ObjectId) []byte {
	bts, _ := id.MarshalJSON()
	return bts
}
