// Generate a first-admin bcrypt hash from stdin on Fedora; never pass a password in argv.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 73))
	if err != nil {
		panic(err)
	}
	password = bytes.TrimSuffix(password, []byte("\n"))
	if len(password) < 16 || len(password) > 72 {
		panic("password must contain 16..72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword(password, 12)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(hash))
}
