package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/uptrace/bun"
)

// verifyBatch bounds how many rows a chain walk holds in memory at once; the
// walk is ordered by id, so batching cannot skip or reorder links.
const verifyBatch = 500

// errChainStartMissing reports that audit_chain_state holds no boundary row.
// Every migrated database has one, so its absence is either a dropped table or a
// deleted row — both of which are tampering, never "the chain has not started".
var errChainStartMissing = errors.New("audit chain start boundary is missing")

// errChainBlanked reports that the newest row carries no hash even though it was
// written after the chain start. Chaining a new row from genesis at that point
// would launder the blanking, so Record refuses instead.
var errChainBlanked = errors.New("audit chain hashes were blanked after the chain start")

// chainGuarantee is repeated in every verdict because "intact" on its own reads
// as a stronger claim than this chain can make, and the API response is the only
// place most operators will ever see the distinction stated.
const chainGuarantee = "tamper-evident, not tamper-proof: this detects partial edits, deletions, and corruption, " +
	"but not a wholesale recompute or a truncated tail by anyone with write access to control.db"

// VerifyResult reports the outcome of a chain walk. FirstBrokenID is the oldest
// row whose link or contents no longer match, which is where an edit, a delete,
// or a hand-inserted row happened. Unhashed counts rows the chain does not
// cover; it is worth reading even when Intact is true, because a chain that was
// blanked and re-based is exactly a chain whose rows are suddenly all unhashed.
type VerifyResult struct {
	Intact        bool   `json:"intact"`
	Checked       int64  `json:"checked"`
	Unhashed      int64  `json:"unhashed"`
	FirstBrokenID *int64 `json:"firstBrokenId,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Guarantee     string `json:"guarantee"`
}

// chainBounds summarises audit_events in one pass so the walk can be checked
// against the recorded boundary before it starts.
type chainBounds struct {
	total  int64
	maxID  int64
	hashed int64
}

// chainStateModel is the single-row boundary between rows written before the
// chain shipped and rows the chain covers. Rows written before it carry an empty
// hash and cannot be retro-hashed honestly — a backfill would only re-seal
// whatever the table already says — so the chain starts after the watermark and
// a legitimately upgraded database still verifies as intact.
type chainStateModel struct {
	bun.BaseModel `bun:"table:audit_chain_state,alias:audit_chain_state"`
	ID            int64 `bun:",pk"`
	Watermark     int64 `bun:",notnull"`
}

// Verify walks the audit chain oldest-first and reports the first row that
// diverges from its recorded hash. A deleted row is caught by the following
// row's link no longer matching; an edited row is caught by its own contents no
// longer hashing to its stored hash; a hash blanked to fake a pre-chain row is
// caught by the recorded chain-start watermark.
//
// The chain is tamper-EVIDENT, not tamper-proof, and only against an attacker
// who cannot rewrite it wholesale. Anyone with write access to control.db can
// delete a row and recompute every hash after it, and this walk will call the
// result intact — as it will for the newest rows simply being truncated, since
// no later link is left to break. Detecting either needs the tail hash anchored
// OUTSIDE this database; see docs/security/audit-chain.md. The boundary checks
// below raise the price of the cheap rewrites, they do not close the class.
func (m *Module) Verify(ctx context.Context) (VerifyResult, error) {
	result, err := m.verify(ctx)
	result.Guarantee = chainGuarantee
	return result, err
}

func (m *Module) verify(ctx context.Context) (VerifyResult, error) {
	watermark, err := chainWatermark(ctx, m.database)
	if errors.Is(err, errChainStartMissing) {
		return VerifyResult{Reason: "the recorded chain start is missing; the audit chain state was deleted or its table was dropped"}, nil
	}
	if err != nil {
		return VerifyResult{}, fmt.Errorf("read audit chain start: %w", err)
	}
	bounds, err := readChainBounds(ctx, m.database)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("read audit chain bounds: %w", err)
	}
	// The migration derives the watermark from MAX(id), and rows are only ever
	// appended, so a boundary past the newest row cannot have arisen honestly: it
	// was pushed forward to pass hashed rows off as pre-chain ones, or the rows it
	// covered were deleted underneath it.
	if watermark > bounds.maxID {
		return VerifyResult{
			Unhashed: bounds.total - bounds.hashed,
			Reason:   "the recorded chain start is newer than the newest audit event; the chain boundary was moved",
		}, nil
	}
	// Blanking every hash and dropping the boundary onto the last row is the
	// two-statement rewrite that would otherwise read as a database that simply
	// predates the chain. In-database it is indistinguishable from a log that was
	// upgraded and has recorded nothing since, so the ambiguity is reported rather
	// than resolved in the attacker's favour.
	if watermark > 0 && bounds.total > 0 && bounds.hashed == 0 {
		return VerifyResult{
			Unhashed: bounds.total,
			Reason: "no audit event carries a chain hash although the chain has started; the hash columns were blanked " +
				"(or, on a database upgraded moments ago, nothing has been recorded since the upgrade)",
		}, nil
	}
	result := VerifyResult{Intact: true}
	previous := chainGenesis(watermark)
	var cursor int64
	for {
		models := make([]eventModel, 0, verifyBatch)
		err := m.database.NewSelect().Model(&models).Where("id > ?", cursor).
			OrderExpr("id ASC").Limit(verifyBatch).Scan(ctx)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("read audit chain: %w", err)
		}
		if len(models) == 0 {
			return result, nil
		}
		for index := range models {
			model := &models[index]
			cursor = model.ID
			if model.Hash == "" {
				// A pre-chain row can only exist at or below the watermark; past it,
				// the row was either inserted by hand or stripped of its hash.
				if model.ID > watermark {
					return result.broken(model.ID, "row carries no chain hash"), nil
				}
				result.Unhashed++
				continue
			}
			if model.PrevHash != previous {
				return result.broken(model.ID, "chain link does not match the preceding row; a row was deleted or reordered"), nil
			}
			if chainHash(previous, model) != model.Hash {
				return result.broken(model.ID, "row contents do not match its recorded hash"), nil
			}
			previous = model.Hash
			result.Checked++
		}
	}
}

func (result VerifyResult) broken(id int64, reason string) VerifyResult {
	result.Intact = false
	result.FirstBrokenID = &id
	result.Reason = reason
	return result
}

// chainWatermark reads the id of the last row that legitimately carries no hash.
func chainWatermark(ctx context.Context, database bun.IDB) (int64, error) {
	var watermark int64
	err := database.NewSelect().Model((*chainStateModel)(nil)).Column("watermark").
		Where("id = ?", 1).Scan(ctx, &watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errChainStartMissing
	}
	if err != nil {
		// Dropping the table is cheaper than deleting its row and just as much
		// tampering, so it must reach the caller as a verdict rather than as an
		// unavailable-database error the operator reads as a bug.
		if missing, probeErr := chainStateTableMissing(ctx, database); probeErr == nil && missing {
			return 0, errChainStartMissing
		}
		return 0, err
	}
	return watermark, nil
}

// chainStateTableMissing asks the schema itself rather than matching on driver
// error text, which is not part of any contract.
func chainStateTableMissing(ctx context.Context, database bun.IDB) (bool, error) {
	var count int64
	err := database.NewSelect().ColumnExpr("count(*)").TableExpr("sqlite_master").
		Where("type = ? AND name = ?", "table", "audit_chain_state").Scan(ctx, &count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// readChainBounds summarises the event table in one statement, so the boundary
// checks cost a single scan regardless of how long the log is.
func readChainBounds(ctx context.Context, database bun.IDB) (chainBounds, error) {
	var bounds chainBounds
	err := database.NewSelect().Model((*eventModel)(nil)).
		ColumnExpr("count(*)").ColumnExpr("coalesce(max(id), 0)").
		ColumnExpr("coalesce(sum(hash <> ''), 0)").
		Scan(ctx, &bounds.total, &bounds.maxID, &bounds.hashed)
	if err != nil {
		return chainBounds{}, err
	}
	return bounds, nil
}

// chainGenesis anchors the first hashed row to the recorded chain start, so the
// boundary is itself covered by every hash that follows: raising the watermark
// to pass later rows off as pre-chain ones invalidates every remaining link.
func chainGenesis(watermark int64) string {
	digest := sha256.Sum256([]byte("nexa-audit-chain-v1:" + strconv.FormatInt(watermark, 10)))
	return hex.EncodeToString(digest[:])
}

// tailHash returns the hash the next row chains from: the newest row's hash, or
// genesis while every row still predates the chain. It reads the newest row
// rather than the newest *hashed* row on purpose — otherwise blanking the hash
// columns would silently restart the chain from genesis and leave the log clean.
func tailHash(ctx context.Context, tx bun.Tx, watermark int64) (string, error) {
	var tail eventModel
	err := tx.NewSelect().Model(&tail).Column("id", "hash").
		OrderExpr("id DESC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return chainGenesis(watermark), nil
	}
	if err != nil {
		return "", err
	}
	if tail.Hash == "" {
		if tail.ID > watermark {
			return "", errChainBlanked
		}
		return chainGenesis(watermark), nil
	}
	return tail.Hash, nil
}

// chainHash digests a row's identity-independent fields — the auto-increment id
// is deliberately excluded so a chain stays verifiable after an export — mixed
// with the previous row's hash. Every field is length-prefixed so no two
// different field splits can produce the same digest input, and the metadata is
// hashed as the exact JSON stored in the row, which encoding/json emits with
// sorted keys.
func chainHash(previous string, model *eventModel) string {
	digest := sha256.New()
	fields := []string{
		previous,
		model.OccurredAt.UTC().Format(time.RFC3339Nano),
		actorField(model.ActorUserID),
		model.Action,
		model.Subject,
		model.RemoteAddress,
		model.Metadata,
	}
	for _, field := range fields {
		digest.Write([]byte(strconv.Itoa(len(field))))
		digest.Write([]byte(":"))
		digest.Write([]byte(field))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// actorField distinguishes "no actor" — a system or pre-authentication event —
// from an actor whose ID is the empty string, which a bare value could not.
func actorField(actor *string) string {
	if actor == nil {
		return "-"
	}
	return "u:" + *actor
}
