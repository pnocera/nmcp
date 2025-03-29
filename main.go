package main

import (
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pnocera/nmcp/pkg/nmcp"
)

func main() {
	// Connect to NATS
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	// Create JetStream context
	js, err := nc.JetStream()
	if err != nil {
		log.Fatal(err)
	}

	// Start Orchestrator
	orch := nmcp.NewOrchestrator(nc, js)
	if err := orch.Start(); err != nil {
		log.Fatal(err)
	}

	// Start Workflow Engine
	wfEngine, err := nmcp.NewWorkflowEngine(nc, js)
	if err != nil {
		log.Fatal(err)
	}

	// Register a sample workflow
	sampleWorkflow := nmcp.WorkflowDefinition{
		ID:          "doc_processing",
		Name:        "Document Processing",
		Description: "Processes documents through OCR, NLP, and classification",
		Version:     "1.0",
		InitialTask: "ocr",
		Tasks: map[string]nmcp.TaskNode{
			"ocr": {
				Type:        "ocr",
				Description: "Optical Character Recognition",
				OnSuccess:   []string{"nlp"},
				OnFailure:   []string{"notify_failure"},
			},
			"nlp": {
				Type:        "nlp",
				Description: "Natural Language Processing",
				OnSuccess:   []string{"classification"},
			},
			"classification": {
				Type:        "classification",
				Description: "Document Classification",
				OnSuccess:   []string{"storage"},
			},
			"storage": {
				Type:        "storage",
				Description: "Store processed document",
			},
			"notify_failure": {
				Type:        "notification",
				Description: "Notify about processing failure",
			},
		},
	}

	if err := wfEngine.RegisterDefinition(sampleWorkflow); err != nil {
		log.Fatal(err)
	}

	// Start some agents
	ocrAgent := nmcp.NewAgent("ocr_1", nc, []string{"ocr"})
	ocrAgent.AddTaskHandler("ocr", func(payload map[string]interface{}) (map[string]interface{}, error) {
		log.Println("Processing OCR task...")
		// Simulate processing time
		time.Sleep(2 * time.Second)
		return map[string]interface{}{
			"text": "This is the extracted text from the document.",
		}, nil
	})
	if err := ocrAgent.Start(); err != nil {
		log.Fatal(err)
	}
	defer ocrAgent.Stop()

	nlpAgent := nmcp.NewAgent("nlp_1", nc, []string{"nlp"})
	nlpAgent.AddTaskHandler("nlp", func(payload map[string]interface{}) (map[string]interface{}, error) {
		log.Println("Processing NLP task...")
		text := payload["text"].(string)
		log.Print(text)
		return map[string]interface{}{
			"entities": []string{"entity1", "entity2"},
			"topics":   []string{"topic1", "topic2"},
		}, nil
	})
	if err := nlpAgent.Start(); err != nil {
		log.Fatal(err)
	}
	defer nlpAgent.Stop()

	// Start a workflow
	workflowID, err := wfEngine.StartWorkflow("doc_processing", map[string]interface{}{
		"document_id": "doc_123",
		"file_path":   "/path/to/document.pdf",
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Started workflow: %s", workflowID)

	// Keep the program running
	select {}
}
