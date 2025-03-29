package nmcp

import (
	"encoding/json"
	"fmt"
	"github.com/nats-io/nats.go"
	"log"
	"time"
)

func (o *Orchestrator) monitorAgentHealth() {
	ticker := time.NewTicker(15 * time.Second) // Check agents every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			o.checkAgentHealth()
		case <-o.shutdownChan:
			return
		}
	}
}

func (o *Orchestrator) checkAgentHealth() {
	o.agentMutex.Lock()
	defer o.agentMutex.Unlock()

	currentTime := time.Now()
	unhealthyThreshold := 30 * time.Second // Consider agent unhealthy after 30s of no heartbeat

	for agentID, agent := range o.agents {
		// Skip if agent is already marked unresponsive
		if agent.Status == "unresponsive" {
			continue
		}

		// Check last heartbeat time
		if currentTime.Sub(agent.LastSeen) > unhealthyThreshold {
			log.Printf("Agent %s is unresponsive - last seen %v", agentID, agent.LastSeen)

			// Mark as unresponsive
			agent.Status = "unresponsive"
			o.agents[agentID] = agent

			// Handle tasks assigned to this agent
			o.reassignAgentTasks(agentID)

			// Notify agent failure
			o.notifyAgentFailure(agentID)
		} else if agent.Status == "busy" && currentTime.Sub(agent.LastSeen) > 15*time.Second {
			// Agent is busy but still sending heartbeats - just log for monitoring
			log.Printf("Agent %s has been busy for %v", agentID, currentTime.Sub(agent.LastSeen))
		}
	}
}

func (o *Orchestrator) reassignAgentTasks(agentID string) {
	kv, err := o.js.KeyValue("task_assignments")
	if err != nil {
		log.Printf("Failed to access task assignments KV: %v", err)
		return
	}

	// Get all tasks assigned to this agent
	assignments, err := kv.Get(agentID)
	if err != nil {
		if err == nats.ErrKeyNotFound {
			return // No tasks assigned
		}
		log.Printf("Failed to get assignments for agent %s: %v", agentID, err)
		return
	}

	var tasks []AssignedTask
	if err := json.Unmarshal(assignments.Value(), &tasks); err != nil {
		log.Printf("Failed to decode assignments for agent %s: %v", agentID, err)
		return
	}

	// Reassign each task
	for _, task := range tasks {
		log.Printf("Reassigning task %s from unresponsive agent %s", task.TaskID, agentID)

		// Get original task message
		taskMsg, err := o.js.GetMsg("TASKS", task.Sequence)
		if err != nil {
			log.Printf("Failed to get original task message %d: %v", task.Sequence, err)
			continue
		}

		// Find a new agent
		newAgentID := o.findAgentForTask(task.Type)
		if newAgentID == "" {
			log.Printf("No available agent for task type %s - sending to dead letter queue", task.Type)
			o.sendToDeadLetterQueue(taskMsg.Data)
			continue
		}

		// Assign to new agent
		subject := fmt.Sprintf("task.assigned.%s", newAgentID)
		if err := o.nc.Publish(subject, taskMsg.Data); err != nil {
			log.Printf("Failed to reassign task %s to agent %s: %v", task.TaskID, newAgentID, err)
			continue
		}

		// Update assignment tracking
		o.updateTaskAssignment(task.TaskID, agentID, newAgentID)
	}

	// Clear assignments for failed agent
	if err := kv.Delete(agentID); err != nil {
		log.Printf("Failed to clear assignments for agent %s: %v", agentID, err)
	}
}

func (o *Orchestrator) notifyAgentFailure(agentID string) {
	failureMsg := map[string]interface{}{
		"agent_id":     agentID,
		"timestamp":    time.Now().Format(time.RFC3339),
		"last_seen":    o.agents[agentID].LastSeen.Format(time.RFC3339),
		"capabilities": o.agents[agentID].Capabilities,
	}

	msgBytes, err := json.Marshal(failureMsg)
	if err != nil {
		log.Printf("Failed to encode agent failure message: %v", err)
		return
	}

	// Publish to agent failure channel
	if err := o.nc.Publish("agent.failed", msgBytes); err != nil {
		log.Printf("Failed to publish agent failure notification: %v", err)
	}

	// Also publish to agent-specific channel
	agentSubject := fmt.Sprintf("agent.%s.failed", agentID)
	if err := o.nc.Publish(agentSubject, msgBytes); err != nil {
		log.Printf("Failed to publish agent-specific failure notification: %v", err)
	}
}

func (o *Orchestrator) updateTaskAssignment(taskID, oldAgentID, newAgentID string) {
	kv, err := o.js.KeyValue("task_assignments")
	if err != nil {
		log.Printf("Failed to access task assignments KV: %v", err)
		return
	}

	// Remove from old agent's assignment list
	if oldAgentID != "" {
		assignments, err := kv.Get(oldAgentID)
		if err == nil {
			var tasks []AssignedTask
			if err := json.Unmarshal(assignments.Value(), &tasks); err == nil {
				newTasks := make([]AssignedTask, 0, len(tasks)-1)
				for _, t := range tasks {
					if t.TaskID != taskID {
						newTasks = append(newTasks, t)
					}
				}
				if newData, err := json.Marshal(newTasks); err == nil {
					kv.Put(oldAgentID, newData)
				}
			}
		}
	}

	// Add to new agent's assignment list
	if newAgentID != "" {
		var tasks []AssignedTask
		assignments, err := kv.Get(newAgentID)
		if err == nil {
			json.Unmarshal(assignments.Value(), &tasks)
		}

		// In reality, we'd need to get the full task details here
		tasks = append(tasks, AssignedTask{
			TaskID: taskID,
			// ... other fields ...
		})

		if newData, err := json.Marshal(tasks); err == nil {
			kv.Put(newAgentID, newData)
		}
	}
}

func (o *Orchestrator) sendToDeadLetterQueue(data []byte) {
	if _, err := o.js.Publish("DEAD_LETTER", data); err != nil {
		log.Printf("Failed to send message to dead letter queue: %v", err)
	}
}
