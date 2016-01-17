package lib

import (
	"crypto/md5"
	"encoding/base64"
	"io"
	"math"
	"os"
)

const filechunk = 8192 // we settle for 8KB

// From: https://www.socketloop.com/tutorials/how-to-generate-checksum-for-file-in-go
// FIXME: Re-use the buffer in the loop to make it faster
func Checksum(filePath string) (string, error) {

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// calculate the file size
	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	filesize := info.Size()
	blocks := uint64(math.Ceil(float64(filesize) / float64(filechunk)))

	hash := md5.New()

	for i := uint64(0); i < blocks; i++ {
		blocksize := int(math.Min(filechunk, float64(filesize-int64(i*filechunk))))
		buf := make([]byte, blocksize)

		file.Read(buf)
		io.WriteString(hash, string(buf)) // append into the hash
	}

	return base64.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}
