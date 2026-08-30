package devinit

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"os"
	"path/filepath"
)

// initializeKey сохраняет существующий dev signing key, включая повтор после
// прерывания между private/public writes. Production не вызывает dev-init.
func initializeKey(dir string) error {
	privatePath := filepath.Join(dir, "jwt-private.pem")
	publicPath := filepath.Join(dir, "jwt-public.pem")
	var key *rsa.PrivateKey
	if info, err := os.Lstat(privatePath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("DEV_KEY_INVALID")
		}
		data, e := os.ReadFile(privatePath)
		if e != nil {
			return errors.New("DEV_KEY_INVALID")
		}
		key, e = jwt.ParseRSAPrivateKeyFromPEM(data)
		if e != nil || key.N.BitLen() < 2048 {
			return errors.New("DEV_KEY_INVALID")
		}
	} else if os.IsNotExist(err) {
		// Нельзя молча заменить публичный trust root новым private key.
		if _, e := os.Lstat(publicPath); !os.IsNotExist(e) {
			return errors.New("DEV_KEY_PAIR_MISMATCH")
		}
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return errors.New("DEV_RANDOM_FAILED")
		}
		if err = create(privatePath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})); err != nil {
			return err
		}
	} else {
		return errors.New("DEV_KEY_UNAVAILABLE")
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return errors.New("DEV_KEY_INVALID")
	}
	expected := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})
	if info, err := os.Lstat(publicPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("DEV_KEY_INVALID")
		}
		actual, e := os.ReadFile(publicPath)
		if e != nil || !bytes.Equal(actual, expected) {
			return errors.New("DEV_KEY_PAIR_MISMATCH")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return errors.New("DEV_KEY_UNAVAILABLE")
	}
	return create(publicPath, expected)
}
