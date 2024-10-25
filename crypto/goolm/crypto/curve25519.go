package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/curve25519"

	"maunium.net/go/mautrix/crypto/goolm/libolmpickle"
	"maunium.net/go/mautrix/id"
)

const (
	Curve25519PrivateKeyLength = curve25519.ScalarSize //The length of the private key.
	Curve25519PublicKeyLength  = 32
)

// Curve25519GenerateKey creates a new curve25519 key pair.
func Curve25519GenerateKey() (Curve25519KeyPair, error) {
	privateKeyByte := make([]byte, Curve25519PrivateKeyLength)
	if _, err := rand.Read(privateKeyByte); err != nil {
		return Curve25519KeyPair{}, err
	}

	privateKey := Curve25519PrivateKey(privateKeyByte)
	publicKey, err := privateKey.PubKey()
	return Curve25519KeyPair{
		PrivateKey: Curve25519PrivateKey(privateKey),
		PublicKey:  Curve25519PublicKey(publicKey),
	}, err
}

// Curve25519GenerateFromPrivate creates a new curve25519 key pair with the private key given.
func Curve25519GenerateFromPrivate(private Curve25519PrivateKey) (Curve25519KeyPair, error) {
	publicKey, err := private.PubKey()
	if err != nil {
		return Curve25519KeyPair{}, err
	}
	return Curve25519KeyPair{
		PrivateKey: private,
		PublicKey:  Curve25519PublicKey(publicKey),
	}, nil
}

// Curve25519KeyPair stores both parts of a curve25519 key.
type Curve25519KeyPair struct {
	PrivateKey Curve25519PrivateKey `json:"private,omitempty"`
	PublicKey  Curve25519PublicKey  `json:"public,omitempty"`
}

// B64Encoded returns a base64 encoded string of the public key.
func (c Curve25519KeyPair) B64Encoded() id.Curve25519 {
	return c.PublicKey.B64Encoded()
}

// SharedSecret returns the shared secret between the key pair and the given public key.
func (c Curve25519KeyPair) SharedSecret(pubKey Curve25519PublicKey) ([]byte, error) {
	return c.PrivateKey.SharedSecret(pubKey)
}

// PickleLibOlm pickles the key pair into the encoder.
func (c Curve25519KeyPair) PickleLibOlm(encoder *libolmpickle.Encoder) {
	c.PublicKey.PickleLibOlm(encoder)
	if len(c.PrivateKey) == Curve25519PrivateKeyLength {
		encoder.Write(c.PrivateKey)
	} else {
		encoder.WriteEmptyBytes(Curve25519PrivateKeyLength)
	}
}

// UnpickleLibOlm decodes the unencryted value and populates the key pair accordingly. It returns the number of bytes read.
func (c *Curve25519KeyPair) UnpickleLibOlm(value []byte) (int, error) {
	//unpickle PubKey
	read, err := c.PublicKey.UnpickleLibOlm(value)
	if err != nil {
		return 0, err
	}
	//unpickle PrivateKey
	privKey, readPriv, err := libolmpickle.UnpickleBytes(value[read:], Curve25519PrivateKeyLength)
	if err != nil {
		return read, err
	}
	c.PrivateKey = privKey
	return read + readPriv, nil
}

// Curve25519PrivateKey represents the private key for curve25519 usage
type Curve25519PrivateKey []byte

// Equal compares the private key to the given private key.
func (c Curve25519PrivateKey) Equal(x Curve25519PrivateKey) bool {
	return bytes.Equal(c, x)
}

// PubKey returns the public key derived from the private key.
func (c Curve25519PrivateKey) PubKey() (Curve25519PublicKey, error) {
	return curve25519.X25519(c, curve25519.Basepoint)
}

// SharedSecret returns the shared secret between the private key and the given public key.
func (c Curve25519PrivateKey) SharedSecret(pubKey Curve25519PublicKey) ([]byte, error) {
	return curve25519.X25519(c, pubKey)
}

// Curve25519PublicKey represents the public key for curve25519 usage
type Curve25519PublicKey []byte

// Equal compares the public key to the given public key.
func (c Curve25519PublicKey) Equal(x Curve25519PublicKey) bool {
	return bytes.Equal(c, x)
}

// B64Encoded returns a base64 encoded string of the public key.
func (c Curve25519PublicKey) B64Encoded() id.Curve25519 {
	return id.Curve25519(base64.RawStdEncoding.EncodeToString(c))
}

// PickleLibOlm pickles the public key into the encoder.
func (c Curve25519PublicKey) PickleLibOlm(encoder *libolmpickle.Encoder) {
	if len(c) == Curve25519PublicKeyLength {
		encoder.Write(c)
	} else {
		encoder.WriteEmptyBytes(Curve25519PublicKeyLength)
	}
}

// UnpickleLibOlm decodes the unencryted value and populates the public key accordingly. It returns the number of bytes read.
func (c *Curve25519PublicKey) UnpickleLibOlm(value []byte) (int, error) {
	unpickled, readBytes, err := libolmpickle.UnpickleBytes(value, Curve25519PublicKeyLength)
	if err != nil {
		return 0, err
	}
	*c = unpickled
	return readBytes, nil
}
