package contextgovernance

// DiagnosisContextObservation is the bounded, provider-neutral runtime view of
// Diagnosis prompt-budget decisions. It intentionally contains no prompt or
// evidence content so it can be persisted and exposed to operations safely.
type DiagnosisContextObservation struct {
	PreflightCalls                int              `json:"preflightCalls"`
	PreflightFailureCount         int              `json:"preflightFailureCount"`
	HighWaterTokens               int              `json:"highWaterTokens"`
	AvailableInputTokens          int              `json:"availableInputTokens"`
	HighWaterRatio                float64          `json:"highWaterRatio"`
	ToolResultTruncatedCount      int              `json:"toolResultTruncatedCount"`
	HardWindowBlockedCount        int              `json:"hardWindowBlockedCount"`
	LastEstimatedUpperBoundTokens int              `json:"lastEstimatedUpperBoundTokens"`
	ReportOutputReserveTokens     int              `json:"reportOutputReserveTokens"`
	ToolGrowthReserveTokens       int              `json:"toolGrowthReserveTokens"`
	EstimationMethod              EstimationMethod `json:"estimationMethod,omitempty"`
}
