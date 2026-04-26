package main

import "time"

const (
	ContextKindScratch = "scratch"
	ContextKindRepo    = "repo"

	ContextStateActive   = "active"
	ContextStateArchived = "archived"
	ContextStateError    = "error"

	QueueStatusPending = "pending"
	QueueStatusRunning = "running"
	QueueStatusDone    = "done"
	QueueStatusFailed  = "failed"

	ScheduleKindCron     = "cron"
	ScheduleKindInterval = "interval"
	ScheduleKindOnce     = "once"

	ScheduleStatusActive    = "active"
	ScheduleStatusPaused    = "paused"
	ScheduleStatusCancelled = "cancelled"
	ScheduleStatusCompleted = "completed"
)

type Context struct {
	ID              string
	DisplayName     string
	Kind            string
	State           string
	RepoURL         string
	WorkspaceDir    string
	CodexSession    string
	AgentsEnabled   bool
	ReasoningEffort string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TopicBinding struct {
	ChatID    int64
	TopicID   int
	ContextID string
}

type StoredMessage struct {
	ID                int64
	ChatID            int64
	TopicID           int
	TelegramMessageID int
	SenderID          int64
	SenderName        string
	Text              string
	Caption           string
	ReplyToMessageID  int
	IsBot             bool
	IsAdmin           bool
	CreatedAt         time.Time
}

type Attachment struct {
	ID               int64
	MessageID        int64
	TelegramFileID   string
	TelegramUniqueID string
	LocalPath        string
	WorkspaceRelPath string
	MimeType         string
	OriginalFilename string
	SizeBytes        int64
	CreatedAt        time.Time
}

type QueueItem struct {
	ID                int64
	ContextID         string
	ScheduleID        string
	MessageID         int64
	TelegramMessageID int
	Prompt            string
	Resume            bool
	Status            string
	Attempts          int
	FailureClass      string
	CurrentRunID      int64
	NextRetryAt       *time.Time
	CreatedAt         time.Time
	CompletedAt       *time.Time
}

type Run struct {
	ID              int64
	QueueID         int64
	ContextID       string
	ContainerID     string
	Status          string
	ExitCode        int
	ArtifactDir     string
	StderrPath      string
	LastMessagePath string
	ErrorText       string
	StartedAt       time.Time
	FinishedAt      *time.Time
}

type Schedule struct {
	ID              string
	ContextID       string
	Kind            string
	Status          string
	CronExpr        string
	IntervalSeconds int64
	RunAt           *time.Time
	ScriptPath      string
	Prompt          string
	Enabled         bool
	LastRunAt       *time.Time
	NextRunAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RunResult struct {
	ExitCode        int
	ContainerID     string
	ArtifactDir     string
	ResponsePath    string
	StderrPath      string
	LastMessagePath string
	LastMessage     string
	SessionID       string
	ErrorText       string
	FailureClass    string
	Retryable       bool
	StartedCodex    bool
	Canceled        bool
}

type UsageRecord struct {
	ID           int64
	RunID        int64
	QueueID      int64
	ContextID    string
	Model        string
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	RawJSON      string
	CreatedAt    time.Time
}

type ScheduleRunHistory struct {
	QueueID    int64
	RunID      int64
	Status     string
	ExitCode   int
	CreatedAt  time.Time
	FinishedAt *time.Time
}

type OutboundAction struct {
	ID                string
	RunID             int64
	ContextID         string
	ChatID            int64
	TopicID           int
	SourceMessageID   int
	Type              string
	Ref               string
	PayloadJSON       string
	Status            string
	TelegramMessageID int
	RequiresAdmin     bool
	CreatedAt         time.Time
	ExpiresAt         *time.Time
	CompletedAt       *time.Time
}

type OutboundActionEvent struct {
	ID               int64
	ActionID         string
	TelegramUpdateID int
	UserID           int64
	Value            string
	Accepted         bool
	Reason           string
	CreatedAt        time.Time
}
