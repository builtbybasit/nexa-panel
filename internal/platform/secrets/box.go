package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keySize = 32

type Cipher interface {
	Encrypt(label string, plaintext []byte) (string, error)
	Decrypt(label, encoded string) ([]byte, error)
}

type Box struct {
	aead cipher.AEAD
}

func OpenKeyFile(path string) (*Box, error) {
	if path == "" {
		return nil, errors.New("master key path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve master key path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create master key directory: %w", err)
	}

	key, err := readOrCreateKey(absolute)
	if err != nil {
		return nil, err
	}
	return New(key)
}

func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("master key must be %d bytes", keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Encrypt(label string, plaintext []byte) (string, error) {
	if label == "" {
		return "", errors.New("secret label is required")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate encryption nonce: %w", err)
	}
	ciphertext := b.aead.Seal(nil, nonce, plaintext, []byte(label))
	payload := append(nonce, ciphertext...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (b *Box) Decrypt(label, encoded string) ([]byte, error) {
	if label == "" {
		return nil, errors.New("secret label is required")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, errors.New("encrypted secret is invalid")
	}
	nonce := payload[:b.aead.NonceSize()]
	ciphertext := payload[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(label))
	if err != nil {
		return nil, errors.New("encrypted secret authentication failed")
	}
	return plaintext, nil
}

func readOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("inspect master key: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("master key must be a regular file accessible only by its owner")
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("master key must contain %d bytes", keySize)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}

	key = make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readOrCreateKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create master key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close master key: %w", err)
	}
	return key, nil
}
