package nmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type Orchestrator struct {
	nc           *nats.Conn
	js           nats.JetStreamContext
	workflows    map[string]WorkflowDefinition
	agents       map[string]AgentMetadata
	shutdownChan chan struct{}
	agentMutex   sync.RWMutex
}

type AgentMetadata struct {
	ID           string
	Capabilities []string
	LastSeen     time.Time
	Status       string // "available", "busy", "unresponsive"
}

func NewOrchestrator(nc *nats.Conn, js nats.JetStreamContext) *Orchestrator {
	return &Orchestrator{
		nc:        nc,
		js:        js,
		workflows: make(map[string]WorkflowDefinition),
		agents:    make(map[string]AgentMetadata),
	}
}

func (o *Orchestrator) Start() error {
	// Create JetStream KV bucket for workflow state
	_, err := o.js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: "workflow_state",
	})
	if err != nil {
		return fmt.Errorf("failed to create KV store: %w", err)
	}

	// Subscribe to agent communications
	o.subscribeToAgentTopics()

	// Start workflow monitoring
	go o.monitorWorkflows()

	// Start agent health checks
	go o.monitorAgentHealth()

	return nil
}

func (o *Orchestrator) subscribeToAgentTopics() {
	// Agent registration
	o.nc.Subscribe("agent.register", func(msg *nats.Msg) {
		var registration struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		}
		if err := json.Unmarshal(msg.Data, &registration); err != nil {
			log.Printf("Error decoding agent registration: %v", err)
			return
		}

		o.agentMutex.Lock()
		defer o.agentMutex.Unlock()

		o.agents[registration.ID] = AgentMetadata{
			ID:           registration.ID,
			Capabilities: registration.Capabilities,
			LastSeen:     time.Now(),
			Status:       "available",
		}

		log.Printf("Agent registered: %s with capabilities: %v", registration.ID, registration.Capabilities)
	})

	// Task completion notifications
	o.nc.Subscribe("task.completed", func(msg *nats.Msg) {
		var completion struct {
			TaskID     string                 `json:"task_id"`
			WorkflowID string                 `json:"workflow_id"`
			Result     map[string]interface{} `json:"result"`
			AgentID    string                 `json:"agent_id"`
		}

		if err := json.Unmarshal(msg.Data, &completion); err != nil {
			log.Printf("Error decoding task completion: %v", err)
			return
		}

		// Update workflow state
		o.handleTaskCompletion(completion.WorkflowID, completion.TaskID, completion.Result)

		// Mark agent as available
		o.agentMutex.Lock()
		if agent, exists := o.agents[completion.AgentID]; exists {
			agent.Status = "available"
			o.agents[completion.AgentID] = agent
		}
		o.agentMutex.Unlock()
	})
}

func (o *Orchestrator) handleTaskCompletion(workflowID, taskID string, result map[string]interface{}) {
	// Get workflow state
	kv, err := o.js.KeyValue("workflow_state")
	if err != nil {
		log.Printf("Error accessing KV store: %v", err)
		return
	}

	entry, err := kv.Get(workflowID)
	if err != nil {
		log.Printf("Workflow %s not found: %v", workflowID, err)
		return
	}

	var state WorkflowState
	if err := json.Unmarshal(entry.Value(), &state); err != nil {
		log.Printf("Error decoding workflow state: %v", err)
		return
	}

	// Update state with task result
	state.CompletedTasks[taskID] = result

	// Determine next steps based on workflow definition
	wf, exists := o.workflows[state.DefinitionID]
	if !exists {
		log.Printf("Workflow definition %s not found", state.DefinitionID)
		return
	}

	// Publish next tasks
	for _, nextTask := range wf.GetNextTasks(taskID, state) {
		taskMsg, err := json.Marshal(nextTask)
		if err != nil {
			log.Printf("Error encoding task: %v", err)
			continue
		}

		// Find suitable agent
		agentID := o.findAgentForTask(nextTask.Type)
		if agentID == "" {
			log.Printf("No available agent for task type %s", nextTask.Type)
			continue
		}

		subject := fmt.Sprintf("task.assigned.%s", agentID)
		if err := o.nc.Publish(subject, taskMsg); err != nil {
			log.Printf("Error publishing task: %v", err)
		}
	}

	// Save updated state
	stateBytes, err := json.Marshal(state)
	if err != nil {
		log.Printf("Error encoding workflow state: %v", err)
		return
	}

	if _, err := kv.Put(workflowID, stateBytes); err != nil {
		log.Printf("Error saving workflow state: %v", err)
	}
}

func (o *Orchestrator) findAgentForTask(taskType string) string {
	o.agentMutex.RLock()
	defer o.agentMutex.RUnlock()

	for id, agent := range o.agents {
		if agent.Status != "available" {
			continue
		}

		for _, capability := range agent.Capabilities {
			if capability == taskType {
				return id
			}
		}
	}
	return ""
}
