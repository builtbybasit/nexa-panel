package deploy

import (
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"

	"github.com/uptrace/bun"
)

type sshAccessModel struct {
	bun.BaseModel `bun:"table:site_ssh_access,alias:site_ssh_access"`
	SiteID        string `bun:",pk"`
	Enabled       bool
	Username      string
	Shell         string
	EnabledAt     *time.Time
	UpdatedAt     time.Time
}

// sshKeyModel is one installed public key. Only public material is stored: a
// private half never reaches the control plane, so a control.db backup cannot
// leak a login.
type sshKeyModel struct {
	bun.BaseModel `bun:"table:site_ssh_keys,alias:site_ssh_keys"`
	ID            string `bun:",pk"`
	SiteID        string
	Label         string
	Algorithm     string
	PublicKey     string
	Fingerprint   string
	Comment       string
	CreatedAt     time.Time
}

// deployKeyModel is the site's GitHub deploy key. Like sshKeyModel it holds
// public material only: the pair is minted on the node and the private half
// never leaves it, so this table has no ciphertext column and a control.db
// backup cannot leak repository access.
type deployKeyModel struct {
	bun.BaseModel `bun:"table:site_deploy_keys,alias:site_deploy_keys"`
	SiteID        string `bun:",pk"`
	Algorithm     string
	PublicKey     string
	Fingerprint   string
	KeyVersion    int
	Repository    string
	LastTestedAt  *time.Time
	LastTestOK    *bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// SSHAccess is the per-site SSH state returned to the UI. AuthorizedKeysPath is
// part of the shape on purpose: the key file lives outside the site home, and
// an operator inspecting the node needs to be told where.
type SSHAccess struct {
	SiteID             string     `json:"siteId"`
	Enabled            bool       `json:"enabled"`
	Username           string     `json:"username"`
	Host               string     `json:"host"`
	Port               int        `json:"port"`
	Shell              string     `json:"shell"`
	AuthorizedKeysPath string     `json:"authorizedKeysPath"`
	EnabledAt          *time.Time `json:"enabledAt,omitempty"`
	Keys               []SSHKey   `json:"keys"`
}

// SSHKey is one key as the UI lists it. The blob is deliberately absent: the
// fingerprint is what a human compares against `ssh-keygen -lf`, and shipping
// the body to every reader of the page buys nothing.
type SSHKey struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Algorithm   string    `json:"algorithm"`
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// DeployKey is the site's GitHub deploy key as the page shows it. Present is
// explicit so the UI can tell "no key yet" from "a key whose public half is
// empty" without inspecting the other fields, and the public half is included
// on purpose: registering it on GitHub is the whole point of showing it.
type DeployKey struct {
	SiteID       string     `json:"siteId"`
	Present      bool       `json:"present"`
	Algorithm    string     `json:"algorithm,omitempty"`
	PublicKey    string     `json:"publicKey,omitempty"`
	Fingerprint  string     `json:"fingerprint,omitempty"`
	KeyVersion   int        `json:"keyVersion"`
	Repository   string     `json:"repository"`
	LastTestedAt *time.Time `json:"lastTestedAt,omitempty"`
	LastTestOK   *bool      `json:"lastTestOk,omitempty"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
}

func randomKeyID() string { return "sshkey_" + secureid.Hex(12) }
