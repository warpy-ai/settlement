package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// APIServer handles HTTP requests for the supervisor
type APIServer struct {
	supervisor     *Supervisor
	router         *mux.Router
	tasks          map[string]*TaskResponse
	batches        map[string]*BatchTaskResponse
	tasksMutex     sync.RWMutex
	batchMutex     sync.RWMutex
	startTime      time.Time
	sessionManager *SessionManager
}

// NewAPIServer creates a new API server instance
func NewAPIServer(supervisor *Supervisor) *APIServer {
	sessionManager := NewSessionManager()
	sessionManager.StartCleanupRoutine()

	server := &APIServer{
		supervisor:     supervisor,
		router:         mux.NewRouter(),
		tasks:          make(map[string]*TaskResponse),
		batches:        make(map[string]*BatchTaskResponse),
		startTime:      time.Now(),
		sessionManager: sessionManager,
	}

	// Register routes
	server.setupRoutes()
	return server
}

func (s *APIServer) setupRoutes() {
	// Session management endpoints (new)
	s.router.HandleFunc("/api/v1/init", s.handleInit).Methods("POST")
	s.router.HandleFunc("/api/v1/sessions/{token}", s.handleGetSession).Methods("GET")
	s.router.HandleFunc("/api/v1/sessions/{token}", s.handleDeleteSession).Methods("DELETE")
	s.router.HandleFunc("/api/v1/submit", s.handleSessionSubmit).Methods("POST")

	// Task management endpoints (legacy - still works without session)
	s.router.HandleFunc("/api/v1/tasks", s.handleSubmitTask).Methods("POST")
	s.router.HandleFunc("/api/v1/tasks/{taskId}", s.handleGetTaskStatus).Methods("GET")
	s.router.HandleFunc("/api/v1/tasks/{taskId}/cancel", s.handleCancelTask).Methods("POST")

	// Batch operations
	s.router.HandleFunc("/api/v1/batch", s.handleSubmitBatch).Methods("POST")
	s.router.HandleFunc("/api/v1/batch/{batchId}", s.handleGetBatchStatus).Methods("GET")
	s.router.HandleFunc("/api/v1/batch/{batchId}/cancel", s.handleCancelBatch).Methods("POST")

	// System management
	s.router.HandleFunc("/api/v1/stats", s.handleGetStats).Methods("GET")
	s.router.HandleFunc("/api/v1/workers", s.handleGetWorkers).Methods("GET")

	// Add middleware
	s.router.Use(s.loggingMiddleware)
	s.router.Use(s.recoveryMiddleware)
}

// ServeHTTP implements http.Handler interface, delegating to the router
func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Start starts the HTTP server
func (s *APIServer) Start(port int) error {
	addr := fmt.Sprintf(":%d", port)
	log.Printf("[APIServer] Starting server on %s", addr)
	return http.ListenAndServe(addr, s.router)
}

func (s *APIServer) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	var submission TaskSubmission
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&submission); err != nil {
		log.Printf("[APIServer] Error decoding request body: %v", err)
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	// Validate submission
	if err := s.validateTaskSubmission(&submission); err != nil {
		s.sendError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Create task response
	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	taskResponse := &TaskResponse{
		TaskID:      taskID,
		Status:      "pending",
		Progress:    0,
		WorkerCount: s.getWorkerCount(&submission),
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Store task response
	s.tasksMutex.Lock()
	s.tasks[taskID] = taskResponse
	s.tasksMutex.Unlock()

	// Process task in background
	go s.processTask(taskID, &submission)

	// Log the task response for debugging
	log.Printf("[APIServer] Sending task response: TaskID=%s, Status=%s, WorkerCount=%d", taskResponse.TaskID, taskResponse.Status, taskResponse.WorkerCount)

	// Return immediate response
	s.sendJSON(w, http.StatusAccepted, taskResponse)
}

func (s *APIServer) validateTaskSubmission(submission *TaskSubmission) error {
	if submission.Content == "" {
		return fmt.Errorf("task content is required")
	}

	if submission.Rules != nil {
		if submission.Rules.MinimumAgreement != nil {
			if *submission.Rules.MinimumAgreement < 0 || *submission.Rules.MinimumAgreement > 1 {
				return fmt.Errorf("minimum_agreement must be between 0 and 1")
			}
		}

		if submission.Rules.MatchStrategy != nil {
			switch *submission.Rules.MatchStrategy {
			case ExactMatch, SemanticMatch, NumericMatch:
				// Valid strategy
			default:
				return fmt.Errorf("invalid match_strategy: must be exact_match, semantic_match, or numeric_match")
			}
		}

		if submission.Rules.NumericTolerance != nil && *submission.Rules.NumericTolerance <= 0 {
			return fmt.Errorf("numeric_tolerance must be greater than 0")
		}
	}

	if submission.Constraints != nil {
		if submission.Constraints.WorkerCount != nil {
			if *submission.Constraints.WorkerCount < 3 || *submission.Constraints.WorkerCount > 15 {
				return fmt.Errorf("worker_count must be between 3 and 15")
			}
			if *submission.Constraints.WorkerCount%2 == 0 {
				return fmt.Errorf("worker_count must be an odd number")
			}
		}

		if submission.Constraints.Priority != nil {
			if *submission.Constraints.Priority < 1 || *submission.Constraints.Priority > 5 {
				return fmt.Errorf("priority must be between 1 and 5")
			}
		}

		if submission.Constraints.RetryAttempts != nil {
			if *submission.Constraints.RetryAttempts < 0 || *submission.Constraints.RetryAttempts > 5 {
				return fmt.Errorf("retry_attempts must be between 0 and 5")
			}
		}
	}

	return nil
}

func (s *APIServer) getWorkerCount(submission *TaskSubmission) int {
	if submission.Constraints != nil && submission.Constraints.WorkerCount != nil {
		return *submission.Constraints.WorkerCount
	}
	return s.supervisor.numWorkers // Default worker count
}

func (s *APIServer) processTask(taskID string, submission *TaskSubmission) {
	// Update task status
	s.updateTaskStatus(taskID, "processing", nil)

	// Analyze task requirements first
	ctx := context.Background()
	requirements, err := AnalyzeTask(ctx, submission.Content, s.supervisor.apiKey)
	if err != nil {
		log.Printf("[APIServer] Failed to analyze task: %v", err)
		s.updateTaskStatus(taskID, "failed", err)
		return
	}

	// Scale workers if needed
	if err := s.supervisor.scaleWorkers(ctx, requirements.WorkerCount); err != nil {
		log.Printf("[APIServer] Failed to scale workers: %v", err)
		s.updateTaskStatus(taskID, "failed", err)
		return
	}

	// Create instruction from submission with the API's taskID
	instruction := &Instruction{
		TaskID:           taskID, // Use the API's taskID, not a new one
		Content:          submission.Content,
		WorkerCount:      requirements.WorkerCount,
		ModelPreferences: submission.ModelPreferences, // Pass through model preferences
		Consensus: ConsensusConfig{
			MinimumAgreement:       requirements.MinimumAgreement,
			TimeoutDuration:        time.Duration(requirements.TimeoutSeconds) * time.Second,
			VotingStrategy:         "majority",
			MatchStrategy:          requirements.MatchStrategy,
			NumericTolerance:       requirements.NumericTolerance,
			ExtractMergedReasoning: true, // Always extract conversational response
		},
	}

	// Apply custom rules if provided (override analyzed requirements)
	if submission.Rules != nil {
		if submission.Rules.MinimumAgreement != nil {
			instruction.Consensus.MinimumAgreement = *submission.Rules.MinimumAgreement
		}
		if submission.Rules.MatchStrategy != nil {
			instruction.Consensus.MatchStrategy = *submission.Rules.MatchStrategy
		}
		if submission.Rules.NumericTolerance != nil {
			instruction.Consensus.NumericTolerance = *submission.Rules.NumericTolerance
		}
	}

	// Apply constraints if provided
	if submission.Constraints != nil {
		if submission.Constraints.WorkerCount != nil {
			instruction.WorkerCount = *submission.Constraints.WorkerCount
		}
		if submission.Constraints.Timeout != nil {
			instruction.Consensus.TimeoutDuration = submission.Constraints.Timeout.ToDuration()
		}
	}

	// Add instruction directly to queue manager (this ensures taskID matches)
	if err := s.supervisor.queueManager.AddInstruction(instruction); err != nil {
		log.Printf("[APIServer] Failed to queue task: %v", err)
		s.updateTaskStatus(taskID, "failed", err)
		return
	}

	// Monitor results
	resultsChan := s.supervisor.GetResults()
	for result := range resultsChan {
		if result.Error != nil {
			s.updateTaskStatus(taskID, "failed", result.Error)
			return
		}
		s.updateTaskStatus(taskID, "completed", nil)
		s.updateTaskResult(taskID, result.Result)
		return
	}
}

func (s *APIServer) updateTaskStatus(taskID, status string, err error) {
	s.tasksMutex.Lock()
	defer s.tasksMutex.Unlock()

	if task, exists := s.tasks[taskID]; exists {
		task.Status = status
		task.UpdatedAt = time.Now()
		if err != nil {
			task.Error = err.Error()
		}
	}
}

func (s *APIServer) updateTaskResult(taskID, result string) {
	s.tasksMutex.Lock()
	defer s.tasksMutex.Unlock()

	if task, exists := s.tasks[taskID]; exists {
		task.Result = result
		task.Progress = 100
		task.UpdatedAt = time.Now()
	}
}

func (s *APIServer) handleGetTaskStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	taskID := vars["taskId"]

	s.tasksMutex.RLock()
	task, exists := s.tasks[taskID]
	s.tasksMutex.RUnlock()

	if !exists {
		s.sendError(w, http.StatusNotFound, "Task not found")
		return
	}

	// Get worker statuses from queue manager - this updates in real-time
	if s.supervisor != nil && s.supervisor.queueManager != nil {
		workerStatusInfos := s.supervisor.queueManager.GetWorkerStatuses(taskID)
		workerStatuses := make([]WorkerStatus, 0, len(workerStatusInfos))
		for _, info := range workerStatusInfos {
			// Get provider/model from worker state if available
			provider := info.Provider
			model := info.Model
			
			// If not in status info, try to get from pool manager
			if provider == "" || model == "" {
				if workerState, err := s.supervisor.poolManager.GetWorkerByID(info.WorkerID); err == nil {
					if provider == "" {
						provider = workerState.Provider
					}
					if model == "" {
						model = workerState.Model
					}
				}
			}
			
			workerStatuses = append(workerStatuses, WorkerStatus{
				WorkerID:  info.WorkerID,
				Status:    info.Status,
				Progress:  info.Progress,
				Reasoning: info.Reasoning,
				Decision:  info.Decision,
				Provider:  provider,
				Model:     model,
				UpdatedAt: info.UpdatedAt,
			})
		}
		task.Metadata.WorkerStatuses = workerStatuses
		
		// Update progress based on worker statuses
		if len(workerStatuses) > 0 {
			totalProgress := 0.0
			for _, ws := range workerStatuses {
				totalProgress += ws.Progress
			}
			task.Progress = (totalProgress / float64(len(workerStatuses))) * 100
		}
	}

	s.sendJSON(w, http.StatusOK, task)
}

func (s *APIServer) handleSubmitBatch(w http.ResponseWriter, r *http.Request) {
	var submission BatchTaskSubmission
	if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
		s.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if len(submission.Tasks) == 0 {
		s.sendError(w, http.StatusBadRequest, "Batch must contain at least one task")
		return
	}

	// Generate batch ID if not provided
	if submission.BatchID == "" {
		submission.BatchID = fmt.Sprintf("batch-%d", time.Now().UnixNano())
	}

	// Create batch response
	batchResponse := &BatchTaskResponse{
		BatchID:   submission.BatchID,
		Status:    "pending",
		TaskIDs:   make([]string, len(submission.Tasks)),
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Store batch response
	s.batchMutex.Lock()
	s.batches[submission.BatchID] = batchResponse
	s.batchMutex.Unlock()

	// Process batch in background
	go s.processBatch(&submission, batchResponse)

	// Return immediate response
	s.sendJSON(w, http.StatusAccepted, batchResponse)
}

func (s *APIServer) processBatch(submission *BatchTaskSubmission, response *BatchTaskResponse) {
	var wg sync.WaitGroup
	parallel := submission.Parallel != nil && *submission.Parallel

	for i, task := range submission.Tasks {
		if parallel {
			wg.Add(1)
			go func(idx int, t TaskSubmission) {
				defer wg.Done()
				s.processBatchTask(submission, response, idx, t)
			}(i, task)
		} else {
			s.processBatchTask(submission, response, i, task)
		}
	}

	if parallel {
		wg.Wait()
	}

	s.updateBatchStatus(response.BatchID, "completed", nil)
}

func (s *APIServer) processBatchTask(submission *BatchTaskSubmission, response *BatchTaskResponse, index int, task TaskSubmission) {
	// Apply common rules and constraints
	if submission.CommonRules != nil {
		if task.Rules == nil {
			task.Rules = submission.CommonRules
		}
	}
	if submission.Constraints != nil {
		if task.Constraints == nil {
			task.Constraints = submission.Constraints
		}
	}

	// Create task ID
	taskID := fmt.Sprintf("%s-task-%d", response.BatchID, index)
	response.TaskIDs[index] = taskID

	// Process individual task
	s.processTask(taskID, &task)
}

func (s *APIServer) handleGetBatchStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	batchID := vars["batchId"]

	s.batchMutex.RLock()
	batch, exists := s.batches[batchID]
	s.batchMutex.RUnlock()

	if !exists {
		s.sendError(w, http.StatusNotFound, "Batch not found")
		return
	}

	// Collect task results
	batch.TaskResults = make([]TaskResponse, len(batch.TaskIDs))
	for i, taskID := range batch.TaskIDs {
		s.tasksMutex.RLock()
		if task, exists := s.tasks[taskID]; exists {
			batch.TaskResults[i] = *task
		}
		s.tasksMutex.RUnlock()
	}

	s.sendJSON(w, http.StatusOK, batch)
}

func (s *APIServer) handleGetStats(w http.ResponseWriter, r *http.Request) {
	total, available := s.supervisor.poolManager.GetWorkerCount()
	load := s.supervisor.poolManager.GetWorkerLoad()

	stats := SupervisorStats{
		ActiveWorkers:    available,
		TotalWorkers:     total,
		SystemLoad:       load,
		UptimeSeconds:    int64(time.Since(s.startTime).Seconds()),
		LastScalingEvent: "N/A", // TODO: Track scaling events
	}

	s.sendJSON(w, http.StatusOK, stats)
}

func (s *APIServer) handleGetWorkers(w http.ResponseWriter, r *http.Request) {
	workers := s.supervisor.poolManager.GetWorkersByStatus("")
	s.sendJSON(w, http.StatusOK, workers)
}

func (s *APIServer) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement task cancellation
	s.sendError(w, http.StatusNotImplemented, "Task cancellation not implemented")
}

func (s *APIServer) handleCancelBatch(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement batch cancellation
	s.sendError(w, http.StatusNotImplemented, "Batch cancellation not implemented")
}

// handleInit creates a new session with worker configuration
func (s *APIServer) handleInit(w http.ResponseWriter, r *http.Request) {
	var req SessionInitRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		log.Printf("[APIServer] Error decoding init request: %v", err)
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	// Create session
	session, err := s.sessionManager.CreateSession(&req)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Build worker configs summary
	workerConfigs := make(map[int]string)
	for i := 0; i < session.WorkerCount; i++ {
		cfg := session.GetWorkerConfig(i)
		provider := cfg.Provider
		if provider == "" || provider == "auto" {
			provider = "auto"
		}
		model := cfg.Model
		if model == "" || model == "auto" {
			model = "auto"
		}
		workerConfigs[i] = fmt.Sprintf("%s/%s", provider, model)
	}

	response := SessionInitResponse{
		Token:         session.Token,
		WorkerCount:   session.WorkerCount,
		WorkerConfigs: workerConfigs,
		AllowOverride: session.AllowOverride,
		ExpiresAt:     session.ExpiresAt,
		CreatedAt:     session.CreatedAt,
	}

	log.Printf("[APIServer] Created session %s with %d workers", session.Token[:16]+"...", session.WorkerCount)
	s.sendJSON(w, http.StatusCreated, response)
}

// handleGetSession returns session configuration
func (s *APIServer) handleGetSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	token := vars["token"]

	session, err := s.sessionManager.GetSession(token)
	if err != nil {
		s.sendError(w, http.StatusNotFound, err.Error())
		return
	}

	s.sendJSON(w, http.StatusOK, session)
}

// handleDeleteSession deletes a session
func (s *APIServer) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	token := vars["token"]

	_, err := s.sessionManager.GetSession(token)
	if err != nil {
		s.sendError(w, http.StatusNotFound, err.Error())
		return
	}

	s.sessionManager.DeleteSession(token)
	s.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSessionSubmit handles task submission with a session token
func (s *APIServer) handleSessionSubmit(w http.ResponseWriter, r *http.Request) {
	var req SessionSubmitRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		log.Printf("[APIServer] Error decoding submit request: %v", err)
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	// Validate content
	if req.Content == "" {
		s.sendError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Get session
	session, err := s.sessionManager.GetSession(req.Token)
	if err != nil {
		s.sendError(w, http.StatusUnauthorized, fmt.Sprintf("Invalid or expired session token: %v", err))
		return
	}

	// Check if model override is allowed
	if len(req.ModelPreferences) > 0 && !session.AllowOverride {
		s.sendError(w, http.StatusForbidden, "Model overrides are not allowed for this session")
		return
	}

	// Create task response
	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	taskResponse := &TaskResponse{
		TaskID:      taskID,
		Status:      "pending",
		Progress:    0,
		WorkerCount: session.WorkerCount,
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Store task response
	s.tasksMutex.Lock()
	s.tasks[taskID] = taskResponse
	s.tasksMutex.Unlock()

	// Process task with session config in background
	go s.processSessionTask(taskID, &req, session)

	log.Printf("[APIServer] Submitted task %s with session %s", taskID, req.Token[:16]+"...")
	s.sendJSON(w, http.StatusAccepted, taskResponse)
}

// processSessionTask processes a task using session configuration
func (s *APIServer) processSessionTask(taskID string, req *SessionSubmitRequest, session *SessionConfig) {
	// Update task status
	s.updateTaskStatus(taskID, "processing", nil)

	ctx := context.Background()

	// Build model preferences from session config (can be overridden if allowed)
	// IMPORTANT: This must happen BEFORE starting workers so they use the correct providers
	modelPreferences := make(map[int]string)
	for i := 0; i < session.WorkerCount; i++ {
		cfg := session.GetWorkerConfig(i)
		if !cfg.IsAutoProvider() {
			modelPreferences[i] = cfg.Provider
		}
	}

	// Apply request overrides if allowed
	if session.AllowOverride && len(req.ModelPreferences) > 0 {
		for k, v := range req.ModelPreferences {
			modelPreferences[k] = v
		}
	}

	// Apply model preferences to session's worker configs BEFORE starting workers
	// This ensures workers are started with the correct providers/models
	if len(modelPreferences) > 0 {
		if session.Workers == nil {
			session.Workers = make(map[int]*WorkerConfig)
		}
		for position, provider := range modelPreferences {
			if session.Workers[position] == nil {
				session.Workers[position] = &WorkerConfig{}
			}
			session.Workers[position].Provider = provider
			log.Printf("[APIServer] Set worker %d to provider: %s", position, provider)
		}
	}

	// Start workers on-demand with session config (now with correct providers)
	if err := s.supervisor.StartSessionWorkers(ctx, session); err != nil {
		log.Printf("[APIServer] Failed to start session workers: %v", err)
		s.updateTaskStatus(taskID, "failed", err)
		return
	}

	// Build consensus config from session
	consensusConfig := ConsensusConfig{
		MinimumAgreement:       0.66, // Default
		TimeoutDuration:        60 * time.Second,
		VotingStrategy:         "majority",
		MatchStrategy:          SemanticMatch,
		ExtractMergedReasoning: true,
	}

	if session.ConsensusRules != nil {
		if session.ConsensusRules.MinimumAgreement > 0 {
			consensusConfig.MinimumAgreement = session.ConsensusRules.MinimumAgreement
		}
		if session.ConsensusRules.MatchStrategy != nil {
			consensusConfig.MatchStrategy = *session.ConsensusRules.MatchStrategy
		}
		if session.ConsensusRules.NumericTolerance > 0 {
			consensusConfig.NumericTolerance = session.ConsensusRules.NumericTolerance
		}
		if session.ConsensusRules.Timeout != nil {
			consensusConfig.TimeoutDuration = session.ConsensusRules.Timeout.ToDuration()
		}
	}

	// Apply request rule overrides
	if req.Rules != nil {
		if req.Rules.MinimumAgreement != nil {
			consensusConfig.MinimumAgreement = *req.Rules.MinimumAgreement
		}
		if req.Rules.MatchStrategy != nil {
			consensusConfig.MatchStrategy = *req.Rules.MatchStrategy
		}
		if req.Rules.NumericTolerance != nil {
			consensusConfig.NumericTolerance = *req.Rules.NumericTolerance
		}
	}

	// Build worker system prompts from session
	workerSystemPrompts := make(map[int]string)
	for i := 0; i < session.WorkerCount; i++ {
		cfg := session.GetWorkerConfig(i)
		if !cfg.IsAutoSystemPrompt() {
			workerSystemPrompts[i] = cfg.SystemPrompt
		}
	}

	// Create instruction with session config
	instruction := &Instruction{
		TaskID:             taskID,
		Content:            req.Content,
		WorkerCount:        session.WorkerCount,
		ModelPreferences:   modelPreferences,
		Consensus:          consensusConfig,
		WorkerSystemPrompts: workerSystemPrompts,
	}

	// Apply constraints if provided
	if req.Constraints != nil {
		if req.Constraints.WorkerCount != nil {
			// Cannot override worker count from session
			log.Printf("[APIServer] Worker count override ignored for session task")
		}
		if req.Constraints.Timeout != nil {
			instruction.Consensus.TimeoutDuration = req.Constraints.Timeout.ToDuration()
		}
	}

	// Add instruction to queue manager
	if err := s.supervisor.queueManager.AddInstruction(instruction); err != nil {
		log.Printf("[APIServer] Failed to queue session task: %v", err)
		s.updateTaskStatus(taskID, "failed", err)
		return
	}

	// Monitor results
	resultsChan := s.supervisor.GetResults()
	for result := range resultsChan {
		if result.Error != nil {
			s.updateTaskStatus(taskID, "failed", result.Error)
			return
		}
		s.updateTaskStatus(taskID, "completed", nil)
		s.updateTaskResult(taskID, result.Result)
		return
	}
}

func (s *APIServer) updateBatchStatus(batchID, status string, err error) {
	s.batchMutex.Lock()
	defer s.batchMutex.Unlock()

	if batch, exists := s.batches[batchID]; exists {
		batch.Status = status
		batch.UpdatedAt = time.Now()
		if err != nil {
			batch.Error = err.Error()
		}
	}
}

// Helper functions for HTTP responses
func (s *APIServer) sendJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	
	// Marshal JSON to bytes first to catch encoding errors
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("[APIServer] Error marshaling JSON response: %v", err)
		// Can't change status code after WriteHeader, but we can try to write error
		w.Write([]byte(`{"error":"Failed to encode response"}`))
		return
	}
	
	// Write the JSON data
	if _, err := w.Write(jsonData); err != nil {
		log.Printf("[APIServer] Error writing JSON response: %v", err)
	}
}

func (s *APIServer) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	
	errorResponse := map[string]string{"error": message}
	jsonData, err := json.Marshal(errorResponse)
	if err != nil {
		log.Printf("[APIServer] Error marshaling error response: %v", err)
		w.Write([]byte(`{"error":"Internal server error"}`))
		return
	}
	
	if _, err := w.Write(jsonData); err != nil {
		log.Printf("[APIServer] Error writing error response: %v", err)
	}
}

// Middleware functions
func (s *APIServer) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[APIServer] %s %s %v", r.Method, r.URL.Path, time.Since(start))
	})
}

func (s *APIServer) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[APIServer] Panic recovered: %v", err)
				s.sendError(w, http.StatusInternalServerError, "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
