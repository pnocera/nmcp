package nmcp

import "time"

type TaskMessage struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	WorkflowID string                 `json:"workflow_id"`
	Payload    map[string]interface{} `json:"payload"`
	Context    map[string]interface{} `json:"context"`
	CreatedAt  time.Time              `json:"created_at"`
	ExpiresAt  time.Time              `json:"expires_at,omitempty"`
	Priority   int                    `json:"priority,omitempty"`
	ReplyTo    string                 `json:"reply_to,omitempty"`
	RetryCount int                    `json:"retry_count,omitempty"`
}
type AssignedTask struct {
	TaskID     string    `json:"task_id"`
	WorkflowID string    `json:"workflow_id"`
	Type       string    `json:"type"`
	AssignedAt time.Time `json:"assigned_at"`
	Sequence   uint64    `json:"sequence"` // NATS message sequence
}

type WorkflowState struct {
	WorkflowID     string                 `json:"workflow_id"`
	DefinitionID   string                 `json:"definition_id"`
	Status         string                 `json:"status"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	CompletedTasks map[string]interface{} `json:"completed_tasks"`
	PendingTasks   []string               `json:"pending_tasks"`
	RetryCounts    map[string]int         `json:"retry_counts"`
	Context        map[string]interface{} `json:"context"`
}

type WorkflowDefinition struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description"`
	Tasks       map[string]TaskNode `json:"tasks"`
	InitialTask string              `json:"initial_task"`
	Parameters  map[string]string   `json:"parameters"` // Global workflow parameters
}

type TaskNode struct {
	Type        string            `json:"type"`
	Description string            `json:"description"`
	OnSuccess   []string          `json:"on_success"`   // Next task IDs on success
	OnFailure   []string          `json:"on_failure"`   // Next task IDs on failure
	Parameters  map[string]string `json:"parameters"`   // Task-specific parameters
	RetryPolicy int               `json:"retry_policy"` // Max retry attempts
	Timeout     time.Duration     `json:"timeout"`      // Task timeout duration
	Parallel    bool              `json:"parallel"`     // Whether this can run parallel with siblings
}
