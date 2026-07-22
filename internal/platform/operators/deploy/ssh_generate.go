package deploy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// GeneratedKey is a freshly minted ed25519 pair. The private half exists only
// inside this value: it is returned to the operator exactly once, in the HTTP
// response that asked for it, and is written neither to the node nor to
// control.db. Nothing can hand it out a second time.
type GeneratedKey struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

// GenerateUserKey mints a login key for a site account without installing it.
// Installation still goes through ApplySSHAccess, which stays the only writer
// of authorized_keys, so a generated key that the caller never keeps grants
// nothing.
func (o *SSHHostOperator) GenerateUserKey(ctx context.Context, request SSHAccessRequest) (GeneratedKey, error) {
	if err := request.validate(); err != nil {
		return GeneratedKey{}, err
	}
	return o.system.GenerateKeyPair(ctx, request.Slug+"@nexa")
}

// GenerateKeyPair runs ssh-keygen inside the agent's PrivateTmp and removes
// both halves before returning. The pair is deliberately never written under a
// site root: a key the node kept would outlive the one-time reveal the panel
// promises, and it is the caller's copy that matters.
func (s *SSHHostSystem) GenerateKeyPair(ctx context.Context, comment string) (GeneratedKey, error) {
	directory, err := os.MkdirTemp("", "nexa-keygen-")
	if err != nil {
		return GeneratedKey{}, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "id_ed25519")
	if output, err := s.command(ctx, "ssh-keygen", "-t", "ed25519", "-N", "", "-C", comment, "-f", path); err != nil {
		return GeneratedKey{}, commandError(output, err)
	}
	private, err := os.ReadFile(path)
	if err != nil {
		return GeneratedKey{}, err
	}
	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		return GeneratedKey{}, err
	}
	if len(private) == 0 || len(public) == 0 {
		return GeneratedKey{}, errors.New("ssh-keygen produced an empty key pair")
	}
	return GeneratedKey{PublicKey: strings.TrimSpace(string(public)), PrivateKey: string(private)}, nil
}

func (c *UnixClient) GenerateUserKey(ctx context.Context, request SSHAccessRequest) (GeneratedKey, error) {
	var generated GeneratedKey
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/ssh/keys/generate", request, &generated)
	return generated, err
}
