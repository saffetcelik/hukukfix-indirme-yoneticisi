// genkey creates the RSA key that gives the browser extension a stable ID.
// The public key goes into extension/manifest.json ("key"); the ID it prints
// is what the native messaging host allows. Keep extension-key.pem private:
// it is only needed to pack a .crx.
//
//	go run ./tools/genkey
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	const file = "extension-key.pem"
	var key *rsa.PrivateKey
	if b, err := os.ReadFile(file); err == nil {
		blk, _ := pem.Decode(b)
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			panic(err)
		}
		key = k.(*rsa.PrivateKey)
	} else {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			panic(err)
		}
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(pub)
	id := make([]byte, 32)
	for i := 0; i < 16; i++ {
		id[2*i] = 'a' + sum[i]>>4
		id[2*i+1] = 'a' + sum[i]&15
	}
	fmt.Println("key:", base64.StdEncoding.EncodeToString(pub))
	fmt.Println("id: ", string(id))
}
