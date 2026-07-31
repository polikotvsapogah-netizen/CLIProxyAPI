package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

const GenericSecretsKeyEnv = "CLI_PROXY_SECRETS_KEY"

// EncryptGenericSecretValue encrypts a secret using CLI_PROXY_SECRETS_KEY.
func EncryptGenericSecretValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	keyMaterial := strings.TrimSpace(os.Getenv(GenericSecretsKeyEnv))
	if keyMaterial == "" {
		return "", fmt.Errorf("%s is required to encrypt generic secrets", GenericSecretsKeyEnv)
	}
	key := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	payload := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.StdEncoding.EncodeToString(payload), nil
}

// DecryptGenericSecretValue returns the plaintext secret, accepting legacy
// plaintext values for hand-written configs.
func DecryptGenericSecretValue(secret GenericSecret) (string, error) {
	if strings.TrimSpace(secret.ValueEncrypted) == "" {
		return strings.TrimSpace(secret.Value), nil
	}
	keyMaterial := strings.TrimSpace(os.Getenv(GenericSecretsKeyEnv))
	if keyMaterial == "" {
		return "", fmt.Errorf("%s is required to decrypt generic secrets", GenericSecretsKeyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secret.ValueEncrypted))
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	key := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted secret payload is too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ciphertext := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt generic secret: %w", err)
	}
	return string(plaintext), nil
}
