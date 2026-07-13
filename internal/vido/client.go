// Package vido is Searchy's least-privilege client for the durable Vido bridge.
// Searchy can create owner-bound intents and deliver plans, but cannot read the
// Vido tables or selected source URLs back from Postgres.
package vido

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrExpired      = errors.New("vido intent expired")
	ErrNotOwner     = errors.New("vido intent belongs to another user")
	ErrWrongContext = errors.New("vido intent used from a different message")
)

type Bridge struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Bridge, error) {
	if databaseURL == "" {
		return nil, nil
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse vido bridge url: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect vido bridge: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping vido bridge: %w", err)
	}
	return &Bridge{pool: pool}, nil
}

func (b *Bridge) Close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

func (b *Bridge) Ready() bool { return b != nil && b.pool != nil }

func (b *Bridge) Healthy(ctx context.Context) bool {
	return b.Ready() && b.pool.Ping(ctx) == nil
}

type Intent struct {
	OwnerUserID   int64
	Kind          string
	DeliveryMode  string
	SourceURL     string
	Platform      string
	SourceSurface string
	OriginChatID  *int64
	Username      string
	FirstName     string
	LastName      string
	TelegramLang  string
	ParentJobID   *int64
	TTL           time.Duration
}

type User struct {
	ID           int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
}

func (b *Bridge) MintIntent(ctx context.Context, in Intent) (string, error) {
	if !b.Ready() {
		return "", errors.New("vido bridge unavailable")
	}
	if in.TTL <= 0 {
		in.TTL = 6 * time.Hour
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate vido token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(token))
	_, err := b.pool.Exec(ctx, `SELECT vido.create_download_intent(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		digest[:], in.OwnerUserID, in.Kind, in.DeliveryMode, in.SourceURL,
		in.Platform, in.SourceSurface, time.Now().Add(in.TTL), in.OriginChatID,
		nilString(in.Username), nilString(in.FirstName), nilString(in.LastName),
		nilString(in.TelegramLang), in.ParentJobID)
	if err != nil {
		return "", fmt.Errorf("mint vido intent: %w", err)
	}
	return token, nil
}

func (b *Bridge) BindIntentMessage(ctx context.Context, token string, owner, chatID int64, messageID int) error {
	if !b.Ready() {
		return errors.New("vido bridge unavailable")
	}
	digest := tokenHash(token)
	var ok bool
	if err := b.pool.QueryRow(ctx,
		`SELECT vido.bind_intent_message($1,$2,$3,$4)`,
		digest, owner, chatID, messageID).Scan(&ok); err != nil {
		return fmt.Errorf("bind vido intent: %w", err)
	}
	if !ok {
		return ErrWrongContext
	}
	return nil
}

type EnqueueArgs struct {
	Token      string
	ActorID    int64
	ChatID     int64
	ThreadID   int
	MessageID  int
	RequestKey string
}

type JobState struct {
	JobID         int64
	Status        string
	ActivityStage string
	ErrorReason   string
	MessageKey    string
	Retryable     bool
}

func (b *Bridge) Enqueue(ctx context.Context, a EnqueueArgs) (JobState, error) {
	if !b.Ready() {
		return JobState{}, errors.New("vido bridge unavailable")
	}
	var state JobState
	err := b.pool.QueryRow(ctx,
		`SELECT job_id, job_status, activity_stage FROM vido.enqueue_searchy_job($1,$2,$3,$4,$5,$6)`,
		tokenHash(a.Token), a.ActorID, a.ChatID, nullableInt(a.ThreadID), a.MessageID, a.RequestKey,
	).Scan(&state.JobID, &state.Status, &state.ActivityStage)
	if err != nil {
		return JobState{}, classifyIntentError(err)
	}
	return state, nil
}

func (b *Bridge) JobStage(ctx context.Context, jobID int64) (JobState, error) {
	if !b.Ready() {
		return JobState{}, errors.New("vido bridge unavailable")
	}
	var state JobState
	state.JobID = jobID
	err := b.pool.QueryRow(ctx,
		`SELECT job_status, activity_stage, COALESCE(error_reason,''),
		        COALESCE(user_message_key,''), retryable
		 FROM vido.get_searchy_job_stage($1)`, jobID,
	).Scan(&state.Status, &state.ActivityStage, &state.ErrorReason, &state.MessageKey, &state.Retryable)
	return state, err
}

type Delivery struct {
	JobID           int64
	OwnerUserID     int64
	TargetChatID    int64
	TargetThreadID  int
	OriginMessageID int
	Plan            DeliveryPlan
	DeliveredOps    []string
}

func (b *Bridge) ClaimDelivery(ctx context.Context, workerID string, leaseSeconds int) (*Delivery, error) {
	if !b.Ready() {
		return nil, nil
	}
	var d Delivery
	var threadID, originMessageID *int
	var raw json.RawMessage
	err := b.pool.QueryRow(ctx,
		`SELECT job_id, owner_user_id, target_chat_id, target_thread_id,
		        origin_message_id, delivery_plan, delivered_operation_ids
		 FROM vido.claim_searchy_delivery($1,$2)`, workerID, leaseSeconds,
	).Scan(&d.JobID, &d.OwnerUserID, &d.TargetChatID, &threadID, &originMessageID, &raw, &d.DeliveredOps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim vido delivery: %w", err)
	}
	if threadID != nil {
		d.TargetThreadID = *threadID
	}
	if originMessageID != nil {
		d.OriginMessageID = *originMessageID
	}
	if err := json.Unmarshal(raw, &d.Plan); err != nil {
		return nil, fmt.Errorf("decode vido delivery plan: %w", err)
	}
	return &d, nil
}

type FileRef struct {
	ContentKey   string `json:"content_key"`
	VariantKey   string `json:"variant_key"`
	SendKind     string `json:"send_kind"`
	ItemIndex    int    `json:"item_index"`
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
}

func (b *Bridge) AckOperation(ctx context.Context, workerID string, jobID int64, op Operation, messageID int, refs []FileRef) error {
	refJSON, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	var ok bool
	err = b.pool.QueryRow(ctx,
		`SELECT vido.ack_searchy_operation($1,$2,$3,$4,$5,'{}'::jsonb,$6::jsonb)`,
		workerID, jobID, op.OperationID, op.Type, messageID, refJSON,
	).Scan(&ok)
	if err != nil {
		return fmt.Errorf("ack vido operation: %w", err)
	}
	if !ok {
		return errors.New("vido delivery lease lost")
	}
	return nil
}

func (b *Bridge) FailOperation(ctx context.Context, workerID string, jobID int64, op Operation, reason string, unknown bool) error {
	var ok bool
	err := b.pool.QueryRow(ctx,
		`SELECT vido.fail_searchy_operation($1,$2,$3,$4,$5,$6)`,
		workerID, jobID, op.OperationID, op.Type, reason, unknown,
	).Scan(&ok)
	if err != nil {
		return fmt.Errorf("fail vido operation: %w", err)
	}
	if !ok {
		return errors.New("vido delivery lease lost")
	}
	return nil
}

func (b *Bridge) FinishDelivery(ctx context.Context, workerID string, jobID int64) error {
	var ok bool
	err := b.pool.QueryRow(ctx,
		`SELECT vido.finish_searchy_delivery($1,$2)`, workerID, jobID,
	).Scan(&ok)
	if err != nil {
		return fmt.Errorf("finish vido delivery: %w", err)
	}
	if !ok {
		return errors.New("vido delivery lease lost")
	}
	return nil
}

func (b *Bridge) InvalidateFileRef(ctx context.Context, fileID string) error {
	var ignored bool
	if err := b.pool.QueryRow(ctx,
		`SELECT vido.invalidate_searchy_file_ref($1)`, fileID).Scan(&ignored); err != nil {
		return fmt.Errorf("invalidate vido file ref: %w", err)
	}
	return nil
}

func DeepLink(username, token string) string {
	return fmt.Sprintf("https://t.me/%s?start=ia_%s", username, token)
}

func tokenHash(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func classifyIntentError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Message {
		case "intent_expired":
			return ErrExpired
		case "intent_not_owner":
			return ErrNotOwner
		case "intent_wrong_context":
			return ErrWrongContext
		}
	}
	return fmt.Errorf("enqueue vido job: %w", err)
}

func nilString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
