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

const dbSchemaVersion = 4

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
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		if err := execStatements(db, schemaV2Statements()); err != nil {
			return err
		}
		return setUserVersion(db, dbSchemaVersion)
	}
	if version < 2 {
		if err := migrateV1ToV2(db); err != nil {
			return err
		}
		if err := setUserVersion(db, 2); err != nil {
			return err
		}
		version = 2
	}
	if version < 3 {
		if err := migrateV2ToV3(db); err != nil {
			return err
		}
		if err := setUserVersion(db, 3); err != nil {
			return err
		}
		version = 3
	}
	if version < 4 {
		if err := migrateV3ToV4(db); err != nil {
			return err
		}
		return setUserVersion(db, 4)
	}
	return nil
}

func schemaV2Statements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS contexts (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL CHECK(kind IN ('scratch','repo')),
			state TEXT NOT NULL CHECK(state IN ('active','archived','error')),
			repo_url TEXT NOT NULL DEFAULT '',
			workspace_dir TEXT NOT NULL,
			codex_session TEXT NOT NULL DEFAULT '',
			agents_enabled INTEGER NOT NULL DEFAULT 0,
			reasoning_effort TEXT NOT NULL DEFAULT 'xhigh',
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
			schedule_id TEXT NOT NULL DEFAULT '',
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
			kind TEXT NOT NULL DEFAULT 'cron' CHECK(kind IN ('cron','interval','once')),
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','cancelled','completed')),
			cron_expr TEXT NOT NULL DEFAULT '',
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			run_at TEXT,
			script_path TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run_at TEXT,
			next_run_at TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(status, next_run_at)`,
		`CREATE TABLE IF NOT EXISTS usage_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES runs(id),
			queue_id INTEGER NOT NULL DEFAULT 0,
			context_id TEXT NOT NULL REFERENCES contexts(id),
			model TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			raw_json TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_context ON usage_records(context_id, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_run ON usage_records(run_id)`,
		`CREATE TABLE IF NOT EXISTS context_skills (
			context_id TEXT NOT NULL REFERENCES contexts(id),
			skill_name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY(context_id, skill_name)
		)`,
		`CREATE TABLE IF NOT EXISTS outbound_actions (
			id TEXT PRIMARY KEY,
			run_id INTEGER NOT NULL DEFAULT 0,
			context_id TEXT NOT NULL REFERENCES contexts(id),
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL DEFAULT 0,
			source_message_id INTEGER NOT NULL DEFAULT 0,
			type TEXT NOT NULL,
			ref TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			telegram_msg_id INTEGER NOT NULL DEFAULT 0,
			requires_admin INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			expires_at TEXT,
			completed_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_outbound_ref ON outbound_actions(context_id, run_id, ref)`,
		`CREATE TABLE IF NOT EXISTS outbound_action_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action_id TEXT NOT NULL REFERENCES outbound_actions(id),
			telegram_update_id INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER NOT NULL DEFAULT 0,
			value TEXT NOT NULL DEFAULT '',
			accepted INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
}

func migrateV1ToV2(db *sql.DB) error {
	adds := []struct {
		table string
		col   string
		stmt  string
	}{
		{"contexts", "agents_enabled", `ALTER TABLE contexts ADD COLUMN agents_enabled INTEGER NOT NULL DEFAULT 0`},
		{"queue", "schedule_id", `ALTER TABLE queue ADD COLUMN schedule_id TEXT NOT NULL DEFAULT ''`},
		{"schedules", "kind", `ALTER TABLE schedules ADD COLUMN kind TEXT NOT NULL DEFAULT 'cron'`},
		{"schedules", "status", `ALTER TABLE schedules ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`},
		{"schedules", "interval_seconds", `ALTER TABLE schedules ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0`},
		{"schedules", "run_at", `ALTER TABLE schedules ADD COLUMN run_at TEXT`},
		{"schedules", "script_path", `ALTER TABLE schedules ADD COLUMN script_path TEXT NOT NULL DEFAULT ''`},
		{"schedules", "updated_at", `ALTER TABLE schedules ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`},
	}
	for _, add := range adds {
		ok, err := columnExists(db, add.table, add.col)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec(add.stmt); err != nil {
				return fmt.Errorf("%s.%s: %w", add.table, add.col, err)
			}
		}
	}
	if err := execStatements(db, schemaV2Statements()); err != nil {
		return err
	}
	stmts := []string{
		`UPDATE schedules SET kind='cron' WHERE kind=''`,
		`UPDATE schedules SET status=CASE WHEN enabled=1 THEN 'active' ELSE 'paused' END WHERE status='' OR status IS NULL`,
		`UPDATE schedules SET updated_at=created_at WHERE updated_at=''`,
		`CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(status, next_run_at)`,
	}
	return execStatements(db, stmts)
}

func migrateV2ToV3(db *sql.DB) error {
	ok, err := columnExists(db, "contexts", "reasoning_effort")
	if err != nil {
		return err
	}
	if !ok {
		if _, err := db.Exec(`ALTER TABLE contexts ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT 'xhigh'`); err != nil {
			return fmt.Errorf("contexts.reasoning_effort: %w", err)
		}
	}
	_, err = db.Exec(`UPDATE contexts SET reasoning_effort='xhigh' WHERE reasoning_effort IS NULL OR reasoning_effort=''`)
	return err
}

func migrateV3ToV4(db *sql.DB) error {
	ok, err := columnExists(db, "contexts", "display_name")
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE contexts ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("contexts.display_name: %w", err)
	}
	return nil
}

func execStatements(db *sql.DB, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %q: %w", stmt, err)
		}
	}
	return nil
}

func setUserVersion(db *sql.DB, version int) error {
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
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
	effort, ok := normalizeReasoningEffort(c.ReasoningEffort)
	if !ok {
		effort = defaultReasoningEffort
	}
	_, err := db.ExecContext(ctx, `INSERT INTO contexts(id, display_name, kind, state, repo_url, workspace_dir, codex_session, agents_enabled, reasoning_effort) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.DisplayName, c.Kind, c.State, c.RepoURL, c.WorkspaceDir, c.CodexSession, boolInt(c.AgentsEnabled), effort)
	return err
}

func UpdateContextDisplayName(ctx context.Context, db *sql.DB, contextID, name string) error {
	_, err := db.ExecContext(ctx, `UPDATE contexts SET display_name=?, updated_at=datetime('now') WHERE id=?`, name, contextID)
	return err
}

func UpdateContextSession(ctx context.Context, db *sql.DB, contextID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE contexts SET codex_session=?, updated_at=datetime('now') WHERE id=?`, sessionID, contextID)
	return err
}

func UpdateContextReasoningEffort(ctx context.Context, db *sql.DB, contextID, effort string) error {
	_, err := db.ExecContext(ctx, `UPDATE contexts SET reasoning_effort=?, updated_at=datetime('now') WHERE id=?`, effort, contextID)
	return err
}

func ArchiveContext(ctx context.Context, db *sql.DB, contextID string) error {
	_, err := db.ExecContext(ctx, `UPDATE contexts SET state='archived', updated_at=datetime('now') WHERE id=?`, contextID)
	return err
}

func GetContextByID(ctx context.Context, db *sql.DB, id string) (Context, error) {
	row := db.QueryRowContext(ctx, contextSelectSQL()+` WHERE id=?`, id)
	return scanContext(row)
}

func ResolveContext(ctx context.Context, db *sql.DB, ref string) (Context, error) {
	if c, err := GetContextByID(ctx, db, ref); err == nil {
		return c, nil
	} else if !isNoRows(err) {
		return Context{}, err
	}
	rows, err := db.QueryContext(ctx, contextSelectSQL()+` WHERE display_name=? ORDER BY updated_at DESC`, ref)
	if err != nil {
		return Context{}, err
	}
	defer rows.Close()
	var matches []Context
	for rows.Next() {
		c, err := scanContext(rows)
		if err != nil {
			return Context{}, err
		}
		matches = append(matches, c)
	}
	if err := rows.Err(); err != nil {
		return Context{}, err
	}
	if len(matches) == 0 {
		return Context{}, sql.ErrNoRows
	}
	if len(matches) > 1 {
		return Context{}, fmt.Errorf("context name %q is ambiguous; use the context id", ref)
	}
	return matches[0], nil
}

func ListContexts(ctx context.Context, db *sql.DB) ([]Context, error) {
	rows, err := db.QueryContext(ctx, contextSelectSQL()+` ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Context
	for rows.Next() {
		c, err := scanContext(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetBoundContext(ctx context.Context, db *sql.DB, chatID int64, topicID int) (Context, error) {
	row := db.QueryRowContext(ctx, `SELECT c.id, c.display_name, c.kind, c.state, c.repo_url, c.workspace_dir, c.codex_session, c.agents_enabled, c.reasoning_effort, c.created_at, c.updated_at
		FROM topic_bindings b JOIN contexts c ON c.id=b.context_id WHERE b.chat_id=? AND b.topic_id=?`, chatID, topicID)
	return scanContext(row)
}

func contextSelectSQL() string {
	return `SELECT id, display_name, kind, state, repo_url, workspace_dir, codex_session, agents_enabled, reasoning_effort, created_at, updated_at FROM contexts`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanContext(row scanner) (Context, error) {
	var c Context
	var created, updated string
	var agentsEnabled int
	if err := row.Scan(&c.ID, &c.DisplayName, &c.Kind, &c.State, &c.RepoURL, &c.WorkspaceDir, &c.CodexSession, &agentsEnabled, &c.ReasoningEffort, &created, &updated); err != nil {
		return c, err
	}
	c.AgentsEnabled = agentsEnabled != 0
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
	return EnqueueForSchedule(ctx, db, contextID, "", messageID, telegramMsgID, prompt, resume)
}

func EnqueueForSchedule(ctx context.Context, db *sql.DB, contextID, scheduleID string, messageID int64, telegramMsgID int, prompt string, resume bool) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO queue(context_id, schedule_id, message_id, telegram_msg_id, prompt, resume) VALUES (?, ?, ?, ?, ?, ?)`,
		contextID, scheduleID, nullableID(messageID), telegramMsgID, prompt, boolInt(resume))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func NextPending(ctx context.Context, db *sql.DB) (QueueItem, error) {
	row := db.QueryRowContext(ctx, `SELECT id, context_id, schedule_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at
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
		RETURNING id, context_id, schedule_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at`)
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

func MarkQueueCancelled(ctx context.Context, db *sql.DB, queueID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE queue SET status='failed', failure_class='cancelled', next_retry_at=NULL, completed_at=datetime('now') WHERE id=?`, queueID)
	return err
}

func CancelPendingQueueForContext(ctx context.Context, db *sql.DB, contextID string) (int64, error) {
	res, err := db.ExecContext(ctx, `UPDATE queue SET status='failed', failure_class='cancelled', next_retry_at=NULL, completed_at=datetime('now') WHERE context_id=? AND status='pending'`, contextID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func CancelPendingQueueItem(ctx context.Context, db *sql.DB, contextID string, queueID int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE queue SET status='failed', failure_class='cancelled', next_retry_at=NULL, completed_at=datetime('now') WHERE id=? AND context_id=? AND status='pending'`, queueID, contextID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func QueueCountsForContext(ctx context.Context, db *sql.DB, contextID string) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM queue WHERE context_id=? GROUP BY status`, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{
		QueueStatusPending: 0,
		QueueStatusRunning: 0,
		QueueStatusDone:    0,
		QueueStatusFailed:  0,
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func QueueItemsForContext(ctx context.Context, db *sql.DB, contextID string, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.QueryContext(ctx, `SELECT id, context_id, schedule_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at
		FROM queue WHERE context_id=? ORDER BY id DESC LIMIT ?`, contextID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func GetQueueItemForContext(ctx context.Context, db *sql.DB, contextID string, queueID int64) (QueueItem, error) {
	row := db.QueryRowContext(ctx, `SELECT id, context_id, schedule_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at
		FROM queue WHERE id=? AND context_id=?`, queueID, contextID)
	return scanQueue(row)
}

func LatestFailedQueueForContext(ctx context.Context, db *sql.DB, contextID string) (QueueItem, error) {
	row := db.QueryRowContext(ctx, `SELECT id, context_id, schedule_id, COALESCE(message_id, 0), telegram_msg_id, prompt, resume, status, attempts, failure_class, current_run_id, next_retry_at, created_at, completed_at
		FROM queue WHERE context_id=? AND status='failed' ORDER BY id DESC LIMIT 1`, contextID)
	return scanQueue(row)
}

func RetryQueueItem(ctx context.Context, db *sql.DB, q QueueItem) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO queue(context_id, schedule_id, message_id, telegram_msg_id, prompt, resume) VALUES (?, ?, ?, ?, ?, ?)`,
		q.ContextID, q.ScheduleID, nullableID(q.MessageID), q.TelegramMessageID, q.Prompt, boolInt(q.Resume))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	if s.Kind == "" {
		s.Kind = ScheduleKindCron
	}
	if s.Status == "" {
		s.Status = ScheduleStatusActive
	}
	s.Enabled = s.Status == ScheduleStatusActive
	_, err := db.ExecContext(ctx, `INSERT INTO schedules(id, context_id, kind, status, cron_expr, interval_seconds, run_at, script_path, prompt, enabled, next_run_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ContextID, s.Kind, s.Status, s.CronExpr, s.IntervalSeconds, formatOptionalTime(s.RunAt), s.ScriptPath, s.Prompt, boolInt(s.Enabled), s.NextRunAt.UTC().Format(dbTimeLayout))
	return err
}

func ListSchedules(ctx context.Context, db *sql.DB, contextID string) ([]Schedule, error) {
	rows, err := db.QueryContext(ctx, scheduleSelectSQL()+` WHERE context_id=? ORDER BY created_at`, contextID)
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

func GetSchedule(ctx context.Context, db *sql.DB, id, contextID string) (Schedule, error) {
	row := db.QueryRowContext(ctx, scheduleSelectSQL()+` WHERE id=? AND context_id=?`, id, contextID)
	return scanSchedule(row)
}

func DueSchedules(ctx context.Context, db *sql.DB, now time.Time) ([]Schedule, error) {
	rows, err := db.QueryContext(ctx, scheduleSelectSQL()+` WHERE status='active' AND next_run_at != '' AND next_run_at <= ? ORDER BY next_run_at`, now.UTC().Format(dbTimeLayout))
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

func UpdateScheduleAfterRun(ctx context.Context, db *sql.DB, id string, last time.Time, next *time.Time, status string) error {
	if status == "" {
		status = ScheduleStatusActive
	}
	enabled := status == ScheduleStatusActive
	_, err := db.ExecContext(ctx, `UPDATE schedules SET last_run_at=?, next_run_at=?, status=?, enabled=?, updated_at=datetime('now') WHERE id=?`,
		last.UTC().Format(dbTimeLayout), formatOptionalScheduleTime(next), status, boolInt(enabled), id)
	return err
}

func DeleteSchedule(ctx context.Context, db *sql.DB, id, contextID string) error {
	_, err := db.ExecContext(ctx, `UPDATE schedules SET status='cancelled', enabled=0, updated_at=datetime('now') WHERE id=? AND context_id=?`, id, contextID)
	return err
}

func UpdateScheduleStatus(ctx context.Context, db *sql.DB, id, contextID, status string) error {
	_, err := db.ExecContext(ctx, `UPDATE schedules SET status=?, enabled=?, updated_at=datetime('now') WHERE id=? AND context_id=?`,
		status, boolInt(status == ScheduleStatusActive), id, contextID)
	return err
}

func UpdateSchedule(ctx context.Context, db *sql.DB, s Schedule) error {
	_, err := db.ExecContext(ctx, `UPDATE schedules SET kind=?, status=?, cron_expr=?, interval_seconds=?, run_at=?, script_path=?, prompt=?, enabled=?, next_run_at=?, updated_at=datetime('now') WHERE id=? AND context_id=?`,
		s.Kind, s.Status, s.CronExpr, s.IntervalSeconds, formatOptionalTime(s.RunAt), s.ScriptPath, s.Prompt, boolInt(s.Status == ScheduleStatusActive), s.NextRunAt.UTC().Format(dbTimeLayout), s.ID, s.ContextID)
	return err
}

func ScheduleHistory(ctx context.Context, db *sql.DB, scheduleID, contextID string) ([]ScheduleRunHistory, error) {
	rows, err := db.QueryContext(ctx, `SELECT q.id, COALESCE(r.id, 0), q.status, COALESCE(r.exit_code, 0), q.created_at, r.finished_at
		FROM queue q LEFT JOIN runs r ON r.queue_id=q.id
		WHERE q.schedule_id=? AND q.context_id=?
		ORDER BY q.id DESC LIMIT 20`, scheduleID, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleRunHistory
	for rows.Next() {
		var h ScheduleRunHistory
		var created string
		var finished sql.NullString
		if err := rows.Scan(&h.QueueID, &h.RunID, &h.Status, &h.ExitCode, &created, &finished); err != nil {
			return nil, err
		}
		h.CreatedAt = parseDBTime(created)
		if finished.Valid {
			t := parseDBTime(finished.String)
			h.FinishedAt = &t
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func scheduleSelectSQL() string {
	return `SELECT id, context_id, kind, status, cron_expr, interval_seconds, run_at, script_path, prompt, enabled, last_run_at, next_run_at, created_at, updated_at FROM schedules`
}

func AddUsageRecord(ctx context.Context, db *sql.DB, u UsageRecord) error {
	_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO usage_records(run_id, queue_id, context_id, model, input_tokens, output_tokens, total_tokens, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, u.RunID, u.QueueID, u.ContextID, u.Model, u.InputTokens, u.OutputTokens, u.TotalTokens, u.RawJSON)
	return err
}

func UsageForRun(ctx context.Context, db *sql.DB, runID int64) (UsageRecord, error) {
	row := db.QueryRowContext(ctx, `SELECT id, run_id, queue_id, context_id, model, input_tokens, output_tokens, total_tokens, raw_json, created_at FROM usage_records WHERE run_id=?`, runID)
	return scanUsage(row)
}

func UsageTotalsForContext(ctx context.Context, db *sql.DB, contextID string) (UsageRecord, error) {
	var u UsageRecord
	u.ContextID = contextID
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0)
		FROM usage_records WHERE context_id=?`, contextID).Scan(&u.InputTokens, &u.OutputTokens, &u.TotalTokens)
	return u, err
}

func RecentUsageForContext(ctx context.Context, db *sql.DB, contextID string, limit int) ([]UsageRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, run_id, queue_id, context_id, model, input_tokens, output_tokens, total_tokens, raw_json, created_at
		FROM usage_records WHERE context_id=? ORDER BY id DESC LIMIT ?`, contextID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRecord
	for rows.Next() {
		u, err := scanUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func SetContextSkill(ctx context.Context, db *sql.DB, contextID, skillName string, enabled bool) error {
	if enabled {
		_, err := db.ExecContext(ctx, `INSERT INTO context_skills(context_id, skill_name, enabled) VALUES (?, ?, 1)
			ON CONFLICT(context_id, skill_name) DO UPDATE SET enabled=1`, contextID, skillName)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO context_skills(context_id, skill_name, enabled) VALUES (?, ?, 0)
		ON CONFLICT(context_id, skill_name) DO UPDATE SET enabled=0`, contextID, skillName)
	return err
}

func ListContextSkills(ctx context.Context, db *sql.DB, contextID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT skill_name FROM context_skills WHERE context_id=? AND enabled=1 ORDER BY skill_name`, contextID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func SetContextAgentsEnabled(ctx context.Context, db *sql.DB, contextID string, enabled bool) error {
	_, err := db.ExecContext(ctx, `UPDATE contexts SET agents_enabled=?, updated_at=datetime('now') WHERE id=?`, boolInt(enabled), contextID)
	return err
}

func CreateOutboundAction(ctx context.Context, db *sql.DB, a OutboundAction) error {
	_, err := db.ExecContext(ctx, `INSERT INTO outbound_actions(id, run_id, context_id, chat_id, topic_id, source_message_id, type, ref, payload_json, status, telegram_msg_id, requires_admin, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.RunID, a.ContextID, a.ChatID, a.TopicID, a.SourceMessageID, a.Type, a.Ref, a.PayloadJSON, a.Status, a.TelegramMessageID, boolInt(a.RequiresAdmin), formatOptionalTime(a.ExpiresAt))
	return err
}

func UpdateOutboundActionTelegramMessage(ctx context.Context, db *sql.DB, id string, telegramMsgID int) error {
	_, err := db.ExecContext(ctx, `UPDATE outbound_actions SET telegram_msg_id=? WHERE id=?`, telegramMsgID, id)
	return err
}

func CompleteOutboundAction(ctx context.Context, db *sql.DB, id, status string) error {
	_, err := db.ExecContext(ctx, `UPDATE outbound_actions SET status=?, completed_at=datetime('now') WHERE id=?`, status, id)
	return err
}

func GetOutboundAction(ctx context.Context, db *sql.DB, id string) (OutboundAction, error) {
	row := db.QueryRowContext(ctx, `SELECT id, run_id, context_id, chat_id, topic_id, source_message_id, type, ref, payload_json, status, telegram_msg_id, requires_admin, created_at, expires_at, completed_at
		FROM outbound_actions WHERE id=?`, id)
	return scanOutboundAction(row)
}

func OutboundActionByRef(ctx context.Context, db *sql.DB, contextID string, runID int64, ref string) (OutboundAction, error) {
	row := db.QueryRowContext(ctx, `SELECT id, run_id, context_id, chat_id, topic_id, source_message_id, type, ref, payload_json, status, telegram_msg_id, requires_admin, created_at, expires_at, completed_at
		FROM outbound_actions WHERE context_id=? AND run_id=? AND ref=? ORDER BY created_at DESC LIMIT 1`, contextID, runID, ref)
	return scanOutboundAction(row)
}

func AddOutboundActionEvent(ctx context.Context, db *sql.DB, e OutboundActionEvent) error {
	_, err := db.ExecContext(ctx, `INSERT INTO outbound_action_events(action_id, telegram_update_id, user_id, value, accepted, reason)
		VALUES (?, ?, ?, ?, ?, ?)`, e.ActionID, e.TelegramUpdateID, e.UserID, e.Value, boolInt(e.Accepted), e.Reason)
	return err
}

func scanUsage(row scanner) (UsageRecord, error) {
	var u UsageRecord
	var created string
	if err := row.Scan(&u.ID, &u.RunID, &u.QueueID, &u.ContextID, &u.Model, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.RawJSON, &created); err != nil {
		return u, err
	}
	u.CreatedAt = parseDBTime(created)
	return u, nil
}

func scanOutboundAction(row scanner) (OutboundAction, error) {
	var a OutboundAction
	var requiresAdmin int
	var created string
	var expires, completed sql.NullString
	if err := row.Scan(&a.ID, &a.RunID, &a.ContextID, &a.ChatID, &a.TopicID, &a.SourceMessageID, &a.Type, &a.Ref, &a.PayloadJSON, &a.Status, &a.TelegramMessageID, &requiresAdmin, &created, &expires, &completed); err != nil {
		return a, err
	}
	a.RequiresAdmin = requiresAdmin != 0
	a.CreatedAt = parseDBTime(created)
	if expires.Valid {
		t := parseDBTime(expires.String)
		a.ExpiresAt = &t
	}
	if completed.Valid {
		t := parseDBTime(completed.String)
		a.CompletedAt = &t
	}
	return a, nil
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
	if err := row.Scan(&q.ID, &q.ContextID, &q.ScheduleID, &q.MessageID, &q.TelegramMessageID, &q.Prompt, &resume, &q.Status, &q.Attempts, &q.FailureClass, &q.CurrentRunID, &next, &created, &completed); err != nil {
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
	var runAt, last sql.NullString
	var next, created, updated string
	if err := row.Scan(&s.ID, &s.ContextID, &s.Kind, &s.Status, &s.CronExpr, &s.IntervalSeconds, &runAt, &s.ScriptPath, &s.Prompt, &enabled, &last, &next, &created, &updated); err != nil {
		return s, err
	}
	s.Enabled = enabled != 0
	if runAt.Valid {
		t := parseDBTime(runAt.String)
		s.RunAt = &t
	}
	if last.Valid {
		t := parseDBTime(last.String)
		s.LastRunAt = &t
	}
	s.NextRunAt = parseDBTime(next)
	s.CreatedAt = parseDBTime(created)
	s.UpdatedAt = parseDBTime(updated)
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

func formatOptionalTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(dbTimeLayout)
}

func formatOptionalScheduleTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(dbTimeLayout)
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
