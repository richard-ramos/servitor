package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func OpenDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrateDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrateDB(db *sql.DB) error {
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS contexts (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK(kind IN ('scratch','repo')),
			state TEXT NOT NULL CHECK(state IN ('active','archived','error')),
			repo_url TEXT NOT NULL DEFAULT '',
			workspace_dir TEXT NOT NULL,
			codex_session TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS topic_bindings (
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			context_id TEXT NOT NULL REFERENCES contexts(id),
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY(chat_id, topic_id)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			telegram_msg_id INTEGER NOT NULL,
			sender_id INTEGER NOT NULL DEFAULT 0,
			sender_name TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			caption TEXT NOT NULL DEFAULT '',
			reply_to_msg_id INTEGER NOT NULL DEFAULT 0,
			is_bot INTEGER NOT NULL DEFAULT 0,
			is_admin INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(chat_id, telegram_msg_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_topic ON messages(chat_id, topic_id, telegram_msg_id)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id INTEGER NOT NULL REFERENCES messages(id),
			telegram_file_id TEXT NOT NULL,
			telegram_unique_id TEXT NOT NULL DEFAULT '',
			local_path TEXT NOT NULL,
			workspace_rel_path TEXT NOT NULL,
			mime_type TEXT NOT NULL DEFAULT '',
			original_filename TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_msg ON attachments(message_id)`,
		`CREATE TABLE IF NOT EXISTS queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			context_id TEXT NOT NULL REFERENCES contexts(id),
			message_id INTEGER REFERENCES messages(id),
			telegram_msg_id INTEGER NOT NULL DEFAULT 0,
			prompt TEXT NOT NULL,
			resume INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','done','failed')),
			attempts INTEGER NOT NULL DEFAULT 0,
			failure_class TEXT NOT NULL DEFAULT '',
			current_run_id INTEGER NOT NULL DEFAULT 0,
			next_retry_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			completed_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_queue_status ON queue(status, next_retry_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_queue_context ON queue(context_id, status, id)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue_id INTEGER NOT NULL REFERENCES queue(id),
			context_id TEXT NOT NULL REFERENCES contexts(id),
			container_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			exit_code INTEGER NOT NULL DEFAULT 0,
			artifact_dir TEXT NOT NULL DEFAULT '',
			stderr_path TEXT NOT NULL DEFAULT '',
			last_message_path TEXT NOT NULL DEFAULT '',
			error_text TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			finished_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS seen_updates (
			update_id INTEGER PRIMARY KEY
		)`,
		`CREATE TABLE IF NOT EXISTS schedules (
			id TEXT PRIMARY KEY,
			context_id TEXT NOT NULL REFERENCES contexts(id),
			cron_expr TEXT NOT NULL,
			prompt TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run_at TEXT,
			next_run_at TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_run_at)`,
		`PRAGMA user_version = 1`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %q: %w", stmt, err)
		}
	}
	return nil
}

func MarkSeen(ctx context.Context, db *sql.DB, updateID int) error {
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO seen_updates(update_id) VALUES (?)`, updateID)
	return err
}

func IsSeen(ctx context.Context, db *sql.DB, updateID int) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seen_updates WHERE update_id=?`, updateID).Scan(&n)
	return n > 0, err
}

func CreateContext(ctx context.Context, db *sql.DB, c Context) error {
	_, err := db.ExecContext(ctx, `INSERT INTO contexts(id, kind, state, repo_url, workspace_dir, codex_session) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.State, c.RepoURL, c.WorkspaceDir, c.CodexSession)
	return err
}

func UpdateContextSession(ctx context.Context, db *sql.DB, contextID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE contexts SET codex_session=?, updated_at=datetime('now') WHERE id=?`, sessionID, contextID)
	return err
}

func ArchiveContext(ctx context.Context, db *sql.DB, contextID string) error {
	_, err := db.ExecContext(ctx, `UPDATE contexts SET state='archived', updated_at=datetime('now') WHERE id=?`, contextID)
	return err
}

func GetContextByID(ctx context.Context, db *sql.DB, id string) (Context, error) {
	row := db.QueryRowContext(ctx, `SELECT id, kind, state, repo_url, workspace_dir, codex_session, created_at, updated_at FROM contexts WHERE id=?`, id)
	return scanContext(row)
}

func GetBoundContext(ctx context.Context, db *sql.DB, chatID int64, topicID int) (Context, error) {
	row := db.QueryRowContext(ctx, `SELECT c.id, c.kind, c.state, c.repo_url, c.workspace_dir, c.codex_session, c.created_at, c.updated_at
		FROM topic_bindings b JOIN contexts c ON c.id=b.context_id WHERE b.chat_id=? AND b.topic_id=?`, chatID, topicID)
	return scanContext(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanContext(row scanner) (Context, error) {
	var c Context
	var created, updated string
	if err := row.Scan(&c.ID, &c.Kind, &c.State, &c.RepoURL, &c.WorkspaceDir, &c.CodexSession, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt = parseDBTime(created)
	c.UpdatedAt = parseDBTime(updated)
	return c, nil
}

func BindTopic(ctx context.Context, db *sql.DB, chatID int64, topicID int, contextID string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO topic_bindings(chat_id, topic_id, context_id) VALUES (?, ?, ?)
		ON CONFLICT(chat_id, topic_id) DO UPDATE SET context_id=excluded.context_id`, chatID, topicID, contextID)
	return err
}

func DetachTopic(ctx context.Context, db *sql.DB, chatID int64, topicID int) error {
	_, err := db.ExecContext(ctx, `DELETE FROM topic_bindings WHERE chat_id=? AND topic_id=?`, chatID, topicID)
	return err
}

func StoreMessage(ctx context.Context, db *sql.DB, m StoredMessage) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `INSERT INTO messages(chat_id, topic_id, telegram_msg_id, sender_id, sender_name, text, caption, reply_to_msg_id, is_bot, is_admin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, telegram_msg_id) DO UPDATE SET text=excluded.text
		RETURNING id`,
		m.ChatID, m.TopicID, m.TelegramMessageID, m.SenderID, m.SenderName, m.Text, m.Caption, m.ReplyToMessageID, boolInt(m.IsBot), boolInt(m.IsAdmin)).Scan(&id)
	if err == nil && id != 0 {
		return id, nil
	}
	err = db.QueryRowContext(ctx, `SELECT id FROM messages WHERE chat_id=? AND telegram_msg_id=?`, m.ChatID, m.TelegramMessageID).Scan(&id)
	return id, err
}

func AddAttachment(ctx context.Context, db *sql.DB, a Attachment) error {
	_, err := db.ExecContext(ctx, `INSERT INTO attachments(message_id, telegram_file_id, telegram_unique_id, local_path, workspace_rel_path, mime_type, original_filename, size_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, a.MessageID, a.TelegramFileID, a.TelegramUniqueID, a.LocalPath, a.WorkspaceRelPath, a.MimeType, a.OriginalFilename, a.SizeBytes)
	return err
}

func RecentMessages(ctx context.Context, db *sql.DB, chatID int64, topicID int, limit int) ([]StoredMessage, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, chat_id, topic_id, telegram_msg_id, sender_id, sender_name, text, caption, reply_to_msg_id, is_bot, is_admin, created_at
		FROM messages WHERE chat_id=? AND topic_id=? ORDER BY telegram_msg_id DESC LIMIT ?`, chatID, topicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []StoredMessage
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		rev = append(rev, m)
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, rows.Err()
}

func MessageByTelegramID(ctx context.Context, db *sql.DB, chatID int64, telegramMsgID int) (StoredMessage, error) {
	row := db.QueryRowContext(ctx, `SELECT id, chat_id, topic_id, telegram_msg_id, sender_id, sender_name, text, caption, reply_to_msg_id, is_bot, is_admin, created_at
		FROM messages WHERE chat_id=? AND telegram_msg_id=?`, chatID, telegramMsgID)
	return scanMessage(row)
}

func AttachmentsForMessages(ctx context.Context, db *sql.DB, messageIDs []int64) (map[int64][]Attachment, error) {
	out := make(map[int64][]Attachment)
	if len(messageIDs) == 0 {
		return out, nil
	}
	holders := strings.TrimRight(strings.Repeat("?,", len(messageIDs)), ",")
	args := make([]any, 0, len(messageIDs))
	for _, id := range messageIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, message_id, telegram_file_id, telegram_unique_id, local_path, workspace_rel_path, mime_type, original_filename, size_bytes, created_at
		FROM attachments WHERE message_id IN (`+holders+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attachment
		var created string
		if err := rows.Scan(&a.ID, &a.MessageID, &a.TelegramFileID, &a.TelegramUniqueID, &a.LocalPath, &a.WorkspaceRelPath, &a.MimeType, &a.OriginalFilename, &a.SizeBytes, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseDBTime(created)
		out[a.MessageID] = append(out[a.MessageID], a)
	}
	return out, rows.Err()
}

func Enqueue(ctx context.Context, db *sql.DB, contextID string, messageID int64, telegramMsgID int, prompt string, resume bool) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO queue(context_id, message_id, telegram_msg_id, prompt, resume) VALUES (?, ?, ?, ?, ?)`,
		contextID, nullableID(messageID), telegramMsgID, prompt, boolInt(resume))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func NextPending(ctx context.Context, db *sql.DB) (QueueItem, error) {
	row := db.QueryRowContext(ctx, `SELECT id, context_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at
		FROM queue q
		WHERE status='pending'
		  AND (next_retry_at IS NULL OR next_retry_at <= datetime('now'))
		  AND NOT EXISTS (SELECT 1 FROM queue r WHERE r.context_id=q.context_id AND r.status='running')
		ORDER BY id LIMIT 1`)
	return scanQueue(row)
}

func ClaimNextPending(ctx context.Context, db *sql.DB) (QueueItem, error) {
	row := db.QueryRowContext(ctx, `UPDATE queue SET status='running'
		WHERE id = (
			SELECT id
			FROM queue q
			WHERE status='pending'
			  AND (next_retry_at IS NULL OR next_retry_at <= datetime('now'))
			  AND NOT EXISTS (SELECT 1 FROM queue r WHERE r.context_id=q.context_id AND r.status='running')
			ORDER BY id LIMIT 1
		)
		RETURNING id, context_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at`)
	return scanQueue(row)
}

func MarkQueueRunning(ctx context.Context, db *sql.DB, queueID, runID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE queue SET status='running', current_run_id=? WHERE id=?`, runID, queueID)
	return err
}

func MarkQueueDone(ctx context.Context, db *sql.DB, queueID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE queue SET status='done', completed_at=datetime('now') WHERE id=?`, queueID)
	return err
}

func MarkQueueFailed(ctx context.Context, db *sql.DB, queueID int64, failureClass string, retryable bool, maxRetries int) error {
	if retryable {
		var attempts int
		if err := db.QueryRowContext(ctx, `SELECT attempts FROM queue WHERE id=?`, queueID).Scan(&attempts); err != nil {
			return err
		}
		attempts++
		if attempts < maxRetries {
			delay := time.Duration(5*(1<<attempts)) * time.Second
			_, err := db.ExecContext(ctx, `UPDATE queue SET status='pending', attempts=?, failure_class=?, next_retry_at=? WHERE id=?`,
				attempts, failureClass, time.Now().UTC().Add(delay).Format(dbTimeLayout), queueID)
			return err
		}
	}
	_, err := db.ExecContext(ctx, `UPDATE queue SET status='failed', failure_class=?, completed_at=datetime('now') WHERE id=?`, failureClass, queueID)
	return err
}

func CreateRun(ctx context.Context, db *sql.DB, queueID int64, contextID string, artifactDir string) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO runs(queue_id, context_id, artifact_dir) VALUES (?, ?, ?)`, queueID, contextID, artifactDir)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func FinishRun(ctx context.Context, db *sql.DB, runID int64, result RunResult, status string) error {
	_, err := db.ExecContext(ctx, `UPDATE runs SET container_id=?, status=?, exit_code=?, artifact_dir=?, stderr_path=?, last_message_path=?, error_text=?, finished_at=datetime('now') WHERE id=?`,
		result.ContainerID, status, result.ExitCode, result.ArtifactDir, result.StderrPath, result.LastMessagePath, result.ErrorText, runID)
	return err
}

func LatestRunForContext(ctx context.Context, db *sql.DB, contextID string) (Run, error) {
	row := db.QueryRowContext(ctx, `SELECT id, queue_id, context_id, container_id, status, exit_code, artifact_dir, stderr_path, last_message_path, error_text, started_at, finished_at
		FROM runs WHERE context_id=? ORDER BY id DESC LIMIT 1`, contextID)
	return scanRun(row)
}

func ResetStaleRunning(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `UPDATE queue SET status='failed', failure_class='host_restart', completed_at=datetime('now') WHERE status='running'`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE runs SET status='failed', error_text='host restarted before completion', finished_at=datetime('now') WHERE status='running'`)
	return err
}

func CreateSchedule(ctx context.Context, db *sql.DB, s Schedule) error {
	_, err := db.ExecContext(ctx, `INSERT INTO schedules(id, context_id, cron_expr, prompt, enabled, next_run_at) VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.ContextID, s.CronExpr, s.Prompt, boolInt(s.Enabled), s.NextRunAt.UTC().Format(dbTimeLayout))
	return err
}

func ListSchedules(ctx context.Context, db *sql.DB, contextID string) ([]Schedule, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, context_id, cron_expr, prompt, enabled, last_run_at, next_run_at, created_at FROM schedules WHERE context_id=? ORDER BY created_at`, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func DueSchedules(ctx context.Context, db *sql.DB, now time.Time) ([]Schedule, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, context_id, cron_expr, prompt, enabled, last_run_at, next_run_at, created_at FROM schedules WHERE enabled=1 AND next_run_at <= ? ORDER BY next_run_at`, now.UTC().Format(dbTimeLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func UpdateScheduleAfterRun(ctx context.Context, db *sql.DB, id string, last, next time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE schedules SET last_run_at=?, next_run_at=? WHERE id=?`, last.UTC().Format(dbTimeLayout), next.UTC().Format(dbTimeLayout), id)
	return err
}

func DeleteSchedule(ctx context.Context, db *sql.DB, id, contextID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM schedules WHERE id=? AND context_id=?`, id, contextID)
	return err
}

func scanMessage(row scanner) (StoredMessage, error) {
	var m StoredMessage
	var created string
	var isBot, isAdmin int
	if err := row.Scan(&m.ID, &m.ChatID, &m.TopicID, &m.TelegramMessageID, &m.SenderID, &m.SenderName, &m.Text, &m.Caption, &m.ReplyToMessageID, &isBot, &isAdmin, &created); err != nil {
		return m, err
	}
	m.IsBot = isBot != 0
	m.IsAdmin = isAdmin != 0
	m.CreatedAt = parseDBTime(created)
	return m, nil
}

func scanQueue(row scanner) (QueueItem, error) {
	var q QueueItem
	var resume int
	var next, completed sql.NullString
	var created string
	if err := row.Scan(&q.ID, &q.ContextID, &q.MessageID, &q.TelegramMessageID, &q.Prompt, &resume, &q.Status, &q.Attempts, &q.FailureClass, &q.CurrentRunID, &next, &created, &completed); err != nil {
		return q, err
	}
	q.Resume = resume != 0
	q.CreatedAt = parseDBTime(created)
	if next.Valid {
		t := parseDBTime(next.String)
		q.NextRetryAt = &t
	}
	if completed.Valid {
		t := parseDBTime(completed.String)
		q.CompletedAt = &t
	}
	return q, nil
}

func scanRun(row scanner) (Run, error) {
	var r Run
	var started string
	var finished sql.NullString
	if err := row.Scan(&r.ID, &r.QueueID, &r.ContextID, &r.ContainerID, &r.Status, &r.ExitCode, &r.ArtifactDir, &r.StderrPath, &r.LastMessagePath, &r.ErrorText, &started, &finished); err != nil {
		return r, err
	}
	r.StartedAt = parseDBTime(started)
	if finished.Valid {
		t := parseDBTime(finished.String)
		r.FinishedAt = &t
	}
	return r, nil
}

func scanSchedule(row scanner) (Schedule, error) {
	var s Schedule
	var enabled int
	var last sql.NullString
	var next, created string
	if err := row.Scan(&s.ID, &s.ContextID, &s.CronExpr, &s.Prompt, &enabled, &last, &next, &created); err != nil {
		return s, err
	}
	s.Enabled = enabled != 0
	if last.Valid {
		t := parseDBTime(last.String)
		s.LastRunAt = &t
	}
	s.NextRunAt = parseDBTime(next)
	s.CreatedAt = parseDBTime(created)
	return s, nil
}

const dbTimeLayout = "2006-01-02 15:04:05"

func parseDBTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{dbTimeLayout, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
