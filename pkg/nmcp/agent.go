package nmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

type Agent struct {
	ID           string
	nc           *nats.Conn
	capabilities []string
	taskHandlers map[string]func(payload map[string]interface{}) (map[string]interface{}, error)
	shutdown     chan struct{}
}

func NewAgent(id string, nc *nats.Conn, capabilities []string) *Agent {
	return &Agent{
		ID:           id,
		nc:           nc,
		capabilities: capabilities,
		taskHandlers: make(map[string]func(map[string]interface{}) (map[string]interface{}, error)),
		shutdown:     make(chan struct{}),
	}
}

func (a *Agent) Start() error {
	// Register with orchestrator
	if err := a.register(); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	// Subscribe to assigned tasks
	subject := fmt.Sprintf("task.assigned.%s", a.ID)
	subscription, err := a.nc.Subscribe(subject, func(msg *nats.Msg) {
		var task struct {
			ID         string                 `json:"id"`
			Type       string                 `json:"type"`
			Payload    map[string]interface{} `json:"payload"`
			WorkflowID string                 `json:"workflow_id"`
		}

		if err := json.Unmarshal(msg.Data, &task); err != nil {
			log.Printf("Error decoding task: %v", err)
			return
		}

		log.Printf("Received task %s of type %s", task.ID, task.Type)

		// Process the task
		handler, exists := a.taskHandlers[task.Type]
		if !exists {
			log.Printf("No handler for task type %s", task.Type)
			return
		}

		result, err := handler(task.Payload)
		if err != nil {
			log.Printf("Task processing failed: %v", err)
			a.reportTaskFailure(task.ID, task.WorkflowID, err.Error())
			return
		}

		a.reportTaskCompletion(task.ID, task.WorkflowID, result)
	})

	if err != nil {
		return fmt.Errorf("failed to subscribe to tasks: %w", err)
	}

	// Start heartbeat
	go a.heartbeat()

	// Handle graceful shutdown
	go func() {
		<-a.shutdown
		subscription.Unsubscribe()
		a.nc.Close()
	}()

	return nil
}

func (a *Agent) register() error {
	registration := struct {
		ID           string   `json:"id"`
		Capabilities []string `json:"capabilities"`
		Hostname     string   `json:"hostname"`
	}{
		ID:           a.ID,
		Capabilities: a.capabilities,
		Hostname:     getHostname(),
	}

	data, err := json.Marshal(registration)
	if err != nil {
		return fmt.Errorf("registration encoding failed: %w", err)
	}

	return a.nc.Publish("agent.register", data)
}

func (a *Agent) heartbeat() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			subject := fmt.Sprintf("agent.heartbeat.%s", a.ID)
			if err := a.nc.Publish(subject, []byte("ping")); err != nil {
				log.Printf("Heartbeat failed: %v", err)
			}
		case <-a.shutdown:
			return
		}
	}
}

func (a *Agent) reportTaskCompletion(taskID, workflowID string, result map[string]interface{}) {
	completion := struct {
		TaskID     string                 `json:"task_id"`
		WorkflowID string                 `json:"workflow_id"`
		Result     map[string]interface{} `json:"result"`
		AgentID    string                 `json:"agent_id"`
	}{
		TaskID:     taskID,
		WorkflowID: workflowID,
		Result:     result,
		AgentID:    a.ID,
	}

	data, err := json.Marshal(completion)
	if err != nil {
		log.Printf("Error encoding task completion: %v", err)
		return
	}

	if err := a.nc.Publish("task.completed", data); err != nil {
		log.Printf("Error reporting task completion: %v", err)
	}
}

func (a *Agent) reportTaskFailure(taskID, workflowID, errorMsg string) {
	failure := struct {
		TaskID     string `json:"task_id"`
		WorkflowID string `json:"workflow_id"`
		Error      string `json:"error"`
		AgentID    string `json:"agent_id"`
	}{
		TaskID:     taskID,
		WorkflowID: workflowID,
		Error:      errorMsg,
		AgentID:    a.ID,
	}

	data, err := json.Marshal(failure)
	if err != nil {
		log.Printf("Error encoding task failure: %v", err)
		return
	}

	if err := a.nc.Publish("task.failed", data); err != nil {
		log.Printf("Error reporting task failure: %v", err)
	}
}

func (a *Agent) AddTaskHandler(taskType string, handler func(map[string]interface{}) (map[string]interface{}, error)) {
	a.taskHandlers[taskType] = handler
}

func (a *Agent) Stop() {
	close(a.shutdown)
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}
