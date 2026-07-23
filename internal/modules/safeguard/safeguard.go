// Package safeguard arms and fires the timed automatic reverts that stop a
// lockout-capable host change from becoming a permanent outage. It is the
// backend half of the "confirm or revert" pattern: a caller applies the risky
// change and arms a revert in the same breath, and the revert fires on a server
// timer unless an operator confirms from a still-working panel session inside
// the window.
//
// Three properties are deliberate and are what make this more than a UI dialog:
//
//   - The timer lives here, not in the browser. A closed tab, a dead laptop, or
//     a severed network path cannot disarm it — which is precisely the situation
//     the revert exists for.
//   - Every armed revert is persisted before it is scheduled, and Start
//     reconciles the table on boot: a revert armed by an API process that died
//     one second later still fires, immediately if its deadline has passed.
//   - The revert is state the UI can poll, so an operator always sees how much
//     time is left and how their access comes back.
//
// The package is domain-agnostic. Each feature module registers an executor for
// its domain and stores whatever payload that executor needs to undo the change.
package safeguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

// Revert lifecycle states. A revert leaves "armed" exactly once: an operator
// confirms it away, or the timer fires and it lands in one of the two terminal
// revert states.
const (
	StateArmed        = "armed"
	StateConfirmed    = "confirmed"
	StateReverted     = "reverted"
	StateRevertFailed = "revert_failed"
)

// DefaultWindow is how long an operator has to prove their access still works.
// Two minutes is long enough to open a second terminal and retry SSH, and short
// enough that an operator who has genuinely locked themselves out is not staring
// at a dead server for long.
const DefaultWindow = 2 * time.Minute

// executeTimeout bounds one revert execution so a wedged agent cannot hold the
// guard's shutdown open forever.
const executeTimeout = 2 * time.Minute

// RiskError reports an action refused because it can lock the operator out and
// the risk was not acknowledged. Reasons are safe to show verbatim: they are
// composed by the module from its own view of the node, never from user input.
type RiskError struct {
	Reasons []string
}

func (e *RiskError) Error() string {
	if len(e.Reasons) == 0 {
		return "this change can lock you out of the server"
	}
	return strings.Join(e.Reasons, " ")
}

// Executor undoes one armed change. It returns the id of the job that carries
// the revert, when the domain reverts through the job queue, so the UI can
// follow the revert the same way it follows any other host mutation. Returning
// 0 is valid for a domain that reverts synchronously.
type Executor func(ctx context.Context, revert Revert) (jobID int64, err error)

// Revert is one armed automatic revert, and doubles as the DTO the UI polls.
// Payload is the domain's own undo instruction and is never rendered, so it is
// not serialised outward.
type Revert struct {
	ID          string          `json:"id"`
	Domain      string          `json:"domain"`
	Subject     string          `json:"subject"`
	Summary     string          `json:"summary"`
	Reasons     []string        `json:"reasons"`
	Payload     json.RawMessage `json:"-"`
	ActorUserID *string         `json:"actorUserId,omitempty"`
	JobID       int64           `json:"jobId,omitempty"`
	RevertJobID int64           `json:"revertJobId,omitempty"`
	ArmedAt     time.Time       `json:"armedAt"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	ConfirmedAt *time.Time      `json:"confirmedAt,omitempty"`
	State       string          `json:"state"`
	Failure     string          `json:"failure,omitempty"`
}

// Armed reports whether the revert is still counting down.
func (r Revert) Armed() bool { return r.State == StateArmed }

// ArmRequest describes the revert to arm alongside a risky change.
type ArmRequest struct {
	// Domain selects the registered executor.
	Domain string
	// Subject names the concrete thing at risk ("22/tcp", "nginx.service").
	Subject string
	// Summary is the one-line sentence the UI shows about what will be undone.
	Summary string
	// Reasons is why the change was judged lockout-capable.
	Reasons []string
	// Payload is the executor's undo instruction.
	Payload any
	// JobID is the job applying the risky change, for cross-referencing.
	JobID       int64
	ActorUserID *string
}

type revertModel struct {
	bun.BaseModel `bun:"table:lockout_reverts,alias:lockout_revert"`
	ID            string `bun:",pk"`
	Domain        string
	Subject       string
	Summary       string
	ReasonsJSON   string
	PayloadJSON   string
	ActorUserID   *string `bun:"actor_user_id"`
	JobID         int64   `bun:"job_id"`
	RevertJobID   int64   `bun:"revert_job_id"`
	ArmedAt       time.Time
	ExpiresAt     time.Time
	ConfirmedAt   *time.Time
	State         string
	Failure       string
}

// Guard owns the armed reverts for every domain that registers with it.
type Guard struct {
	database *bun.DB
	logger   *slog.Logger
	window   time.Duration
	now      func() time.Time

	mu        sync.Mutex
	executors map[string]Executor
	timers    map[string]*time.Timer
	closed    bool
	baseCtx   context.Context
	cancel    context.CancelFunc
	running   sync.WaitGroup
	closeOnce sync.Once
}

// Option configures a Guard at construction.
type Option func(*Guard)

// WithWindow overrides the confirmation window. Tests use a short one; the
// control plane uses DefaultWindow.
func WithWindow(window time.Duration) Option {
	return func(g *Guard) {
		if window > 0 {
			g.window = window
		}
	}
}

func New(database *bun.DB, logger *slog.Logger, options ...Option) (*Guard, error) {
	if database == nil {
		return nil, errors.New("safeguard requires a control database")
	}
	if logger == nil {
		logger = slog.Default()
	}
	guard := &Guard{
		database:  database,
		logger:    logger,
		window:    DefaultWindow,
		now:       time.Now,
		executors: map[string]Executor{},
		timers:    map[string]*time.Timer{},
		baseCtx:   context.Background(),
	}
	for _, option := range options {
		option(guard)
	}
	return guard, nil
}

// Window is how long a newly armed revert waits before firing.
func (g *Guard) Window() time.Duration { return g.window }

// Register attaches the executor that undoes one domain's changes. Registration
// happens during module construction, before Start, so a reconciled revert
// always finds its executor.
func (g *Guard) Register(domain string, executor Executor) error {
	if domain == "" || executor == nil {
		return errors.New("safeguard domain and executor are required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.executors[domain]; exists {
		return fmt.Errorf("safeguard domain %q is already registered", domain)
	}
	g.executors[domain] = executor
	return nil
}

// Arm persists a revert and starts its countdown. It is called after the risky
// change has been queued: an undo that runs against a change that never applied
// is harmless in both current domains (re-adding a rule that still exists, or
// starting a service that never stopped), whereas failing to arm before the
// change lands would leave a real outage unguarded.
func (g *Guard) Arm(ctx context.Context, request ArmRequest) (Revert, error) {
	if request.Domain == "" || request.Subject == "" {
		return Revert{}, errors.New("safeguard domain and subject are required")
	}
	payload, err := json.Marshal(request.Payload)
	if err != nil {
		return Revert{}, fmt.Errorf("encode revert payload: %w", err)
	}
	reasons, err := json.Marshal(request.Reasons)
	if err != nil {
		return Revert{}, fmt.Errorf("encode revert reasons: %w", err)
	}
	armedAt := g.now().UTC()
	model := &revertModel{
		ID: "revert_" + secureid.Hex(8), Domain: request.Domain, Subject: request.Subject,
		Summary: request.Summary, ReasonsJSON: string(reasons), PayloadJSON: string(payload),
		ActorUserID: request.ActorUserID, JobID: request.JobID,
		ArmedAt: armedAt, ExpiresAt: armedAt.Add(g.window), State: StateArmed,
	}
	if _, err := g.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Revert{}, fmt.Errorf("arm automatic revert: %w", err)
	}
	revert := toRevert(model)
	g.schedule(revert)
	return revert, nil
}

// Confirm disarms a revert. Reaching this method at all is the proof the guard
// wants: the request travelled an authenticated panel session, so the access
// path the change might have broken demonstrably still works.
func (g *Guard) Confirm(ctx context.Context, domain, id string) (Revert, error) {
	model, err := g.load(ctx, domain, id)
	if err != nil {
		return Revert{}, err
	}
	if model.State != StateArmed {
		return toRevert(model), nil
	}
	confirmedAt := g.now().UTC()
	result, err := g.database.NewUpdate().Model((*revertModel)(nil)).
		Set("state = ?", StateConfirmed).Set("confirmed_at = ?", confirmedAt).
		Where("id = ? AND state = ?", id, StateArmed).Exec(ctx)
	if err != nil {
		return Revert{}, fmt.Errorf("confirm automatic revert: %w", err)
	}
	// A zero-row update means the timer won the race and already fired; report
	// the stored outcome rather than claiming a confirmation that did not happen.
	if affected, _ := result.RowsAffected(); affected == 0 {
		return g.Get(ctx, domain, id)
	}
	g.stopTimer(id)
	model.State, model.ConfirmedAt = StateConfirmed, &confirmedAt
	return toRevert(model), nil
}

// Get returns one revert scoped to its domain.
func (g *Guard) Get(ctx context.Context, domain, id string) (Revert, error) {
	model, err := g.load(ctx, domain, id)
	if err != nil {
		return Revert{}, err
	}
	return toRevert(model), nil
}

// ErrNotFound reports an unknown revert id.
var ErrNotFound = errors.New("that automatic revert does not exist")

func (g *Guard) load(ctx context.Context, domain, id string) (*revertModel, error) {
	model := new(revertModel)
	query := g.database.NewSelect().Model(model).Where("id = ?", id)
	if domain != "" {
		query = query.Where("domain = ?", domain)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, ErrNotFound
	}
	return model, nil
}

// Recent lists a domain's reverts newest first, so the page can show both the
// armed countdown and the outcome of the one that just fired.
func (g *Guard) Recent(ctx context.Context, domain string, limit int) ([]Revert, error) {
	if limit <= 0 {
		limit = 20
	}
	var models []revertModel
	if err := g.database.NewSelect().Model(&models).
		Where("domain = ?", domain).OrderExpr("armed_at DESC").Limit(limit).Scan(ctx); err != nil {
		return nil, fmt.Errorf("list automatic reverts: %w", err)
	}
	reverts := make([]Revert, 0, len(models))
	for index := range models {
		reverts = append(reverts, toRevert(&models[index]))
	}
	return reverts, nil
}

// Start reconciles the stored table into live timers. Every revert still marked
// armed is rescheduled; one whose deadline passed while the API was down fires
// straight away, which is the whole point of persisting it.
func (g *Guard) Start(ctx context.Context) {
	g.mu.Lock()
	if g.cancel != nil {
		g.mu.Unlock()
		return
	}
	g.baseCtx, g.cancel = context.WithCancel(context.WithoutCancel(ctx))
	g.mu.Unlock()

	var models []revertModel
	if err := g.database.NewSelect().Model(&models).Where("state = ?", StateArmed).Scan(ctx); err != nil {
		g.logger.Error("reconcile armed automatic reverts", "error", err)
		return
	}
	for index := range models {
		revert := toRevert(&models[index])
		g.logger.Warn("rescheduling an armed automatic revert after restart",
			"revert", revert.ID, "domain", revert.Domain, "subject", revert.Subject, "expiresAt", revert.ExpiresAt)
		g.schedule(revert)
	}
}

// Close stops the countdowns and waits for any revert already executing.
func (g *Guard) Close() {
	g.closeOnce.Do(func() {
		g.mu.Lock()
		g.closed = true
		stopped := 0
		for id, timer := range g.timers {
			if timer.Stop() {
				// Stop won the race with the deadline, so this timer's body will
				// never run and must surrender its wait-group ticket here.
				stopped++
			}
			delete(g.timers, id)
		}
		cancel := g.cancel
		g.mu.Unlock()
		for i := 0; i < stopped; i++ {
			g.running.Done()
		}
		if cancel != nil {
			cancel()
		}
		g.running.Wait()
	})
}

func (g *Guard) schedule(revert Revert) {
	delay := revert.ExpiresAt.Sub(g.now())
	if delay < 0 {
		delay = 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.timers[revert.ID]; exists {
		return
	}
	// A revert armed during shutdown keeps its row and is rescheduled by the
	// next Start; scheduling a timer the closing guard will never wait on is not.
	if g.closed {
		return
	}
	id := revert.ID
	g.running.Add(1)
	g.timers[id] = time.AfterFunc(delay, func() {
		defer g.running.Done()
		g.fire(id)
	})
}

func (g *Guard) stopTimer(id string) {
	g.mu.Lock()
	timer, exists := g.timers[id]
	delete(g.timers, id)
	g.mu.Unlock()
	if exists && timer.Stop() {
		// Stop won the race, so the AfterFunc body will never run and must not
		// leave its wait-group ticket outstanding.
		g.running.Done()
	}
}

// fire executes one revert. It re-reads the row first: a confirmation committed
// microseconds before the timer expired must win, and after a restart the row is
// the only truth about whether the revert is still wanted.
func (g *Guard) fire(id string) {
	g.mu.Lock()
	delete(g.timers, id)
	baseCtx := g.baseCtx
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(baseCtx, executeTimeout)
	defer cancel()

	model, err := g.load(ctx, "", id)
	if err != nil || model.State != StateArmed {
		return
	}
	revert := toRevert(model)
	g.mu.Lock()
	executor := g.executors[revert.Domain]
	g.mu.Unlock()
	if executor == nil {
		g.finish(ctx, id, 0, fmt.Errorf("no revert executor is registered for %q", revert.Domain))
		return
	}
	g.logger.Warn("automatic revert window expired; undoing the change",
		"revert", revert.ID, "domain", revert.Domain, "subject", revert.Subject)
	jobID, execErr := executor(ctx, revert)
	g.finish(ctx, id, jobID, execErr)
}

func (g *Guard) finish(ctx context.Context, id string, jobID int64, execErr error) {
	state, failure := StateReverted, ""
	if execErr != nil {
		state, failure = StateRevertFailed, execErr.Error()
		g.logger.Error("automatic revert failed", "revert", id, "error", execErr)
	}
	if _, err := g.database.NewUpdate().Model((*revertModel)(nil)).
		Set("state = ?", state).Set("failure = ?", failure).Set("revert_job_id = ?", jobID).
		Where("id = ? AND state = ?", id, StateArmed).Exec(ctx); err != nil {
		g.logger.Error("record automatic revert outcome", "revert", id, "error", err)
	}
}

func toRevert(model *revertModel) Revert {
	var reasons []string
	_ = json.Unmarshal([]byte(model.ReasonsJSON), &reasons)
	return Revert{
		ID: model.ID, Domain: model.Domain, Subject: model.Subject, Summary: model.Summary,
		Reasons: reasons, Payload: json.RawMessage(model.PayloadJSON), ActorUserID: model.ActorUserID,
		JobID: model.JobID, RevertJobID: model.RevertJobID, ArmedAt: model.ArmedAt,
		ExpiresAt: model.ExpiresAt, ConfirmedAt: model.ConfirmedAt, State: model.State,
		Failure: model.Failure,
	}
}
