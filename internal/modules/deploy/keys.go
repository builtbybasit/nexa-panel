package deploy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"

	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

const (
	// maxPublicKeyLength bounds a pasted key. The operator applies the same cap
	// to the blob alone; this one stops an oversized paste before it is split.
	maxPublicKeyLength = 16 * 1024
	maxLabelLength     = 64
	// maxCommentLength matches the operator's own cap on the trailing field.
	maxCommentLength = 128
)

// parseAuthorizedKey splits one pasted public key into the three fields an
// authorized_keys line may carry. It refuses an options field outright: options
// precede the algorithm token and `command=` or `environment=` there is
// arbitrary code execution as the site account, so a line that does not start
// with a bare algorithm word is rejected rather than sanitized.
func parseAuthorizedKey(line string) (algorithm, blob, comment string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", "", errors.New("paste a public key")
	}
	if len(line) > maxPublicKeyLength {
		return "", "", "", errors.New("public key is too long")
	}
	if strings.ContainsAny(line, "\x00\r\n") {
		return "", "", "", errors.New("public key must be a single line")
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", "", errors.New("public key must be an algorithm followed by its body")
	}
	algorithm = fields[0]
	if strings.HasPrefix(algorithm, "-") || strings.Contains(algorithm, "=") || strings.Contains(algorithm, ",") {
		return "", "", "", errors.New("public key options are not accepted")
	}
	// The node's allowlist is applied here, not only there. A key the operator
	// will not install must never be stored: the key list is sent whole on every
	// apply, so one unsupported row would fail every future enable rather than
	// the paste that introduced it.
	if !deployoperator.SupportedKeyAlgorithm(algorithm) {
		return "", "", "", errors.New("that public key type is not supported: use ed25519, RSA, ECDSA (nistp256/384/521), or an ed25519 security key")
	}
	blob = fields[1]
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", "", "", errors.New("public key body is not valid base64")
	}
	// The key's own wire format names its type. Checking it against the
	// algorithm word means the stored algorithm and fingerprint describe the key
	// that was actually pasted, not the label somebody typed in front of it.
	if declared, ok := wireAlgorithm(raw); !ok || declared != algorithm {
		return "", "", "", errors.New("public key body does not match its algorithm")
	}
	comment = strings.Join(fields[2:], " ")
	// The comment is the only caller string that reaches the file, so it carries
	// the operator's own constraints back to the paste box instead of failing on
	// the node one round trip later.
	if len(comment) > maxCommentLength || strings.HasPrefix(comment, "-") {
		return "", "", "", errors.New("public key comment is not acceptable")
	}
	return algorithm, blob, comment, nil
}

// wireAlgorithm reads the leading length-prefixed string of an SSH public key
// blob, which is the key type by definition of the wire encoding.
func wireAlgorithm(raw []byte) (string, bool) {
	if len(raw) < 4 {
		return "", false
	}
	length := binary.BigEndian.Uint32(raw[:4])
	if length == 0 || uint64(length) > uint64(len(raw)-4) {
		return "", false
	}
	return string(raw[4 : 4+length]), true
}

// fingerprint is the SHA256 form OpenSSH prints, so what the panel shows can be
// compared byte for byte against `ssh-keygen -lf` on the operator's own machine.
func fingerprint(blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", errors.New("public key body is not valid base64")
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// normalizeLabel keeps the operator-visible name printable and short; it is
// stored, listed, and audited, never written into an authorized_keys line.
func normalizeLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("give the key a label")
	}
	if len(label) > maxLabelLength {
		return "", errors.New("key label is too long")
	}
	if strings.ContainsAny(label, "\x00\r\n") {
		return "", errors.New("key label contains a control character")
	}
	return label, nil
}
