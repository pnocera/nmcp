package nmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

type WorkflowEngine struct {
	nc *nats.Conn
	js nats.JetStreamContext
	kv nats.KeyValue
}

func NewWorkflowEngine(nc *nats.Conn, js nats.JetStreamContext) (*WorkflowEngine, error) {
	kv, err := js.KeyValue("workflow_state")
	if err != nil {
		return nil, fmt.Errorf("failed to access KV store: %w", err)
	}

	return &WorkflowEngine{
		nc: nc,
		js: js,
		kv: kv,
	}, nil
}

func (w *WorkflowEngine) RegisterDefinition(def WorkflowDefinition) error {
	// Store the definition (in a real implementation, you might use a separate KV bucket)
	log.Printf("Registered workflow definition: %s (%s)", def.Name, def.ID)
	return nil
}

func (w *WorkflowEngine) StartWorkflow(definitionID string, initialPayload map[string]interface{}) (string, error) {
	// Generate workflow ID
	workflowID := fmt.Sprintf("wf_%d", time.Now().UnixNano())

	// Create initial state
	state := WorkflowState{
		WorkflowID:     workflowID,
		DefinitionID:   definitionID,
		Status:         "running",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		CompletedTasks: make(map[string]interface{}),
		Context:        initialPayload,
	}

	// Store initial state
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to encode workflow state: %w", err)
	}

	if _, err := w.kv.Put(workflowID, stateBytes); err != nil {
		return "", fmt.Errorf("failed to store workflow state: %w", err)
	}

	// Publish initial task
	initialTask := map[string]interface{}{
		"workflow_id": workflowID,
		"task_id":     "initial_task",
		"type":        "first_task_type", // This would come from the definition
		"payload":     initialPayload,
	}

	taskBytes, err := json.Marshal(initialTask)
	if err != nil {
		return "", fmt.Errorf("failed to encode initial task: %w", err)
	}

	if err := w.nc.Publish("workflow.task", taskBytes); err != nil {
		return "", fmt.Errorf("failed to publish initial task: %w", err)
	}

	return workflowID, nil
}

func (w *WorkflowEngine) HandleTaskCompletion(workflowID string, taskID string, result map[string]interface{}) error {
	// Get current state
	entry, err := w.kv.Get(workflowID)
	if err != nil {
		return fmt.Errorf("failed to get workflow state: %w", err)
	}

	var state WorkflowState
	if err := json.Unmarshal(entry.Value(), &state); err != nil {
		return fmt.Errorf("failed to decode workflow state: %w", err)
	}

	// Update state
	state.CompletedTasks[taskID] = result
	state.UpdatedAt = time.Now()

	// Determine next tasks based on workflow definition
	// In a real implementation, this would use the workflow definition
	nextTask := map[string]interface{}{
		"workflow_id": workflowID,
		"task_id":     "next_task_" + taskID,
		"type":        "next_task_type",
		"payload":     result,
	}

	// Publish next task
	taskBytes, err := json.Marshal(nextTask)
	if err != nil {
		return fmt.Errorf("failed to encode next task: %w", err)
	}

	if err := w.nc.Publish("workflow.task", taskBytes); err != nil {
		return fmt.Errorf("failed to publish next task: %w", err)
	}

	// Update state
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode updated state: %w", err)
	}

	if _, err := w.kv.Put(workflowID, stateBytes); err != nil {
		return fmt.Errorf("failed to update workflow state: %w", err)
	}

	return nil
}

func (w *WorkflowEngine) GetWorkflowStatus(workflowID string) (WorkflowState, error) {
	entry, err := w.kv.Get(workflowID)
	if err != nil {
		return WorkflowState{}, fmt.Errorf("failed to get workflow state: %w", err)
	}

	var state WorkflowState
	if err := json.Unmarshal(entry.Value(), &state); err != nil {
		return WorkflowState{}, fmt.Errorf("failed to decode workflow state: %w", err)
	}

	return state, nil
}

// GetNextTasks determines which tasks should be executed next based on:
// - The completed task's outcome (success/failure)
// - The workflow definition
// - The current workflow state
func (wf *WorkflowDefinition) GetNextTasks(completedTaskID string, state WorkflowState) []TaskMessage {
	var nextTasks []TaskMessage

	// Get the node definition for the completed task
	completedNode, exists := wf.Tasks[completedTaskID]
	if !exists {
		log.Printf("No definition found for completed task %s", completedTaskID)
		return nil
	}

	// Determine if the task succeeded or failed
	taskResult, taskCompleted := state.CompletedTasks[completedTaskID]
	taskSuccess := false

	if taskCompleted {
		if resultMap, ok := taskResult.(map[string]interface{}); ok {
			if status, ok := resultMap["status"].(string); ok {
				taskSuccess = (status == "success")
			}
		}
	}

	// Get the next task IDs based on success/failure
	var nextTaskIDs []string
	if taskSuccess {
		nextTaskIDs = completedNode.OnSuccess
	} else {
		nextTaskIDs = completedNode.OnFailure
		if len(nextTaskIDs) == 0 {
			// Default failure path if none specified
			nextTaskIDs = []string{"notify_failure"}
		}
	}

	// Create task messages for each next task
	for _, nextTaskID := range nextTaskIDs {
		nextNode, exists := wf.Tasks[nextTaskID]
		if !exists {
			log.Printf("Next task %s not defined in workflow", nextTaskID)
			continue
		}

		// Build task context
		context := make(map[string]interface{})
		for k, v := range state.Context {
			context[k] = v
		}

		// Add completed task's result to context
		context["previous_task"] = completedTaskID
		context["previous_result"] = taskResult

		// Apply parameter mappings
		payload := make(map[string]interface{})
		for paramName, paramValue := range nextNode.Parameters {
			// Handle parameter templates like "{{previous_result.output}}"
			resolvedValue := resolveParameter(paramValue, state.CompletedTasks)
			payload[paramName] = resolvedValue
		}

		// Create the task message
		task := TaskMessage{
			ID:         generateTaskID(),
			Type:       nextNode.Type,
			WorkflowID: state.WorkflowID,
			Payload:    payload,
			Context:    context,
			CreatedAt:  time.Now(),
			RetryCount: state.RetryCounts[nextTaskID],
		}

		// Set timeout if specified
		if nextNode.Timeout > 0 {
			task.ExpiresAt = time.Now().Add(nextNode.Timeout)
		}

		nextTasks = append(nextTasks, task)
	}

	return nextTasks
}

// Helper function to resolve parameter templates
func resolveParameter(template string, completedTasks map[string]interface{}) interface{} {
	// Simple template format: {{task_id.result_field}}
	if strings.HasPrefix(template, "{{") && strings.HasSuffix(template, "}}") {
		path := strings.Trim(template[2:len(template)-2], " ")
		parts := strings.Split(path, ".")

		if len(parts) >= 2 {
			taskID := parts[0]
			fieldPath := parts[1:]

			if result, exists := completedTasks[taskID]; exists {
				return navigateFieldPath(result, fieldPath)
			}
		}
	}
	return template
}

// Helper to navigate nested result structures
func navigateFieldPath(data interface{}, path []string) interface{} {
	current := data

	for _, field := range path {
		if m, ok := current.(map[string]interface{}); ok {
			if val, exists := m[field]; exists {
				current = val
			} else {
				return nil
			}
		} else {
			return nil
		}
	}

	return current
}

func generateTaskID() string {
	return fmt.Sprintf("task_%d", time.Now().UnixNano())
}
