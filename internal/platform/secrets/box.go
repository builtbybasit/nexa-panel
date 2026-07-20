package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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
	rand.Read(nonce)
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
	key, err := readKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}

	key = make([]byte, keySize)
	rand.Read(key)
	file, err := os.CreateTemp(filepath.Dir(path), ".master-key-*.partial")
	if err != nil {
		return nil, fmt.Errorf("create temporary master key: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure temporary master key: %w", err)
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
	if err := os.Link(temporary, path); errors.Is(err, os.ErrExist) {
		return readKey(path)
	} else if err != nil {
		return nil, fmt.Errorf("publish master key without replacing an existing key: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("persist master key directory entry: %w", err)
	}
	return key, nil
}

func readKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master key must be a regular file accessible only by its owner")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("master key must contain %d bytes", keySize)
	}
	return key, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
