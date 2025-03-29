package nmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

func (o *Orchestrator) monitorWorkflows() {
	ticker := time.NewTicker(30 * time.Second) // Check workflows every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			o.checkWorkflowStatuses()
		case <-o.shutdownChan:
			return
		}
	}
}

func (o *Orchestrator) checkWorkflowStatuses() {
	kv, err := o.js.KeyValue("workflow_state")
	if err != nil {
		log.Printf("Failed to access workflow state KV: %v", err)
		return
	}

	keys, err := kv.Keys()
	if err != nil {
		log.Printf("Failed to get workflow keys: %v", err)
		return
	}

	for _, key := range keys {
		entry, err := kv.Get(key)
		if err != nil {
			log.Printf("Failed to get workflow %s: %v", key, err)
			continue
		}

		var state WorkflowState
		if err := json.Unmarshal(entry.Value(), &state); err != nil {
			log.Printf("Failed to decode workflow %s: %v", key, err)
			continue
		}

		// Check for stalled workflows
		if state.Status == "running" && time.Since(state.UpdatedAt) > 5*time.Minute {
			log.Printf("Workflow %s appears stalled - last update %v", key, state.UpdatedAt)
			o.handleStalledWorkflow(key, state)
		}

		// Check for completion
		if o.isWorkflowComplete(state) {
			o.finalizeWorkflow(key, state)
		}
	}
}

func (o *Orchestrator) handleStalledWorkflow(workflowID string, state WorkflowState) {
	// Get the workflow definition
	wf, exists := o.workflows[state.DefinitionID]
	if !exists {
		log.Printf("No definition found for stalled workflow %s", workflowID)
		return
	}

	// Find the last completed task
	var lastTaskID string
	var lastCompletion time.Time
	for taskID, result := range state.CompletedTasks {
		if resultMap, ok := result.(map[string]interface{}); ok {
			if completedAt, ok := resultMap["completed_at"].(string); ok {
				if t, err := time.Parse(time.RFC3339, completedAt); err == nil && t.After(lastCompletion) {
					lastTaskID = taskID
					lastCompletion = t
				}
			}
		}
	}

	// Determine recovery action based on workflow definition
	if lastTaskID != "" {
		taskNode := wf.Tasks[lastTaskID]
		if taskNode.RetryPolicy > 0 && state.RetryCounts[lastTaskID] < taskNode.RetryPolicy {
			// Retry the task
			log.Printf("Retrying task %s for workflow %s", lastTaskID, workflowID)
			o.retryTask(workflowID, lastTaskID, state)
		} else {
			// Follow failure path
			log.Printf("Following failure path for task %s in workflow %s", lastTaskID, workflowID)
			o.followFailurePath(workflowID, lastTaskID, state, wf)
		}
	} else {
		// No tasks completed yet - restart workflow
		log.Printf("Restarting stalled workflow %s from beginning", workflowID)
		o.restartWorkflow(workflowID, state)
	}
}

func (o *Orchestrator) isWorkflowComplete(state WorkflowState) bool {
	wf, exists := o.workflows[state.DefinitionID]
	if !exists {
		return false
	}

	// Check if all terminal tasks are completed
	for taskID, task := range wf.Tasks {
		if len(task.OnSuccess) == 0 && len(task.OnFailure) == 0 {
			if _, completed := state.CompletedTasks[taskID]; !completed {
				return false
			}
		}
	}
	return true
}

func (o *Orchestrator) finalizeWorkflow(workflowID string, state WorkflowState) {
	kv, err := o.js.KeyValue("workflow_state")
	if err != nil {
		log.Printf("Failed to access KV store for workflow completion: %v", err)
		return
	}

	state.Status = "completed"
	state.UpdatedAt = time.Now()

	stateBytes, err := json.Marshal(state)
	if err != nil {
		log.Printf("Failed to encode completed workflow state: %v", err)
		return
	}

	if _, err := kv.Put(workflowID, stateBytes); err != nil {
		log.Printf("Failed to mark workflow %s as completed: %v", workflowID, err)
	}

	// Notify workflow completion
	completionMsg := map[string]interface{}{
		"workflow_id":  workflowID,
		"status":       "completed",
		"completed_at": time.Now().Format(time.RFC3339),
		"results":      state.CompletedTasks,
	}

	msgBytes, err := json.Marshal(completionMsg)
	if err != nil {
		log.Printf("Failed to encode workflow completion message: %v", err)
		return
	}

	subject := fmt.Sprintf("workflow.%s.completed", workflowID)
	if err := o.nc.Publish(subject, msgBytes); err != nil {
		log.Printf("Failed to publish workflow completion: %v", err)
	}
}
