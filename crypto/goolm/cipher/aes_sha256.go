package cipher

import (
	"bytes"
	"io"

	"maunium.net/go/mautrix/crypto/aescbc"
	"maunium.net/go/mautrix/crypto/goolm/crypto"
)

// derivedAESKeys stores the derived keys for the AESSHA256 cipher
type derivedAESKeys struct {
	key     []byte
	hmacKey []byte
	iv      []byte
}

// deriveAESKeys derives three keys for the AESSHA256 cipher
func deriveAESKeys(kdfInfo []byte, key []byte) (derivedAESKeys, error) {
	hkdf := crypto.HKDFSHA256(key, nil, kdfInfo)
	keymatter := make([]byte, 80)
	_, err := io.ReadFull(hkdf, keymatter)
	return derivedAESKeys{
		key:     keymatter[:32],
		hmacKey: keymatter[32:64],
		iv:      keymatter[64:],
	}, err
}

// AESSHA256 is a valid cipher using AES with CBC and HKDFSha256.
type AESSHA256 struct {
	kdfInfo []byte
}

// NewAESSHA256 returns a new AESSHA256 cipher with the key derive function info (kdfInfo).
func NewAESSHA256(kdfInfo []byte) *AESSHA256 {
	return &AESSHA256{
		kdfInfo: kdfInfo,
	}
}

// Encrypt encrypts the plaintext with the key. The key is used to derive the actual encryption key (32 bytes) as well as the iv (16 bytes).
func (c AESSHA256) Encrypt(key, plaintext []byte) (ciphertext []byte, err error) {
	keys, err := deriveAESKeys(c.kdfInfo, key)
	if err != nil {
		return nil, err
	}
	return aescbc.Encrypt(keys.key, keys.iv, plaintext)
}

// Decrypt decrypts the ciphertext with the key. The key is used to derive the actual encryption key (32 bytes) as well as the iv (16 bytes).
func (c AESSHA256) Decrypt(key, ciphertext []byte) (plaintext []byte, err error) {
	keys, err := deriveAESKeys(c.kdfInfo, key)
	if err != nil {
		return nil, err
	}
	return aescbc.Decrypt(keys.key, keys.iv, ciphertext)
}

// MAC returns the MAC for the message using the key. The key is used to derive the actual mac key (32 bytes).
func (c AESSHA256) MAC(key, message []byte) ([]byte, error) {
	keys, err := deriveAESKeys(c.kdfInfo, key)
	if err != nil {
		return nil, err
	}
	return crypto.HMACSHA256(keys.hmacKey, message), nil
}

// Verify checks the MAC of the message using the key against the givenMAC. The key is used to derive the actual mac key (32 bytes).
func (c AESSHA256) Verify(key, message, givenMAC []byte) (bool, error) {
	mac, err := c.MAC(key, message)
	if err != nil {
		return false, err
	}
	return bytes.Equal(givenMAC, mac[:len(givenMAC)]), nil
}
