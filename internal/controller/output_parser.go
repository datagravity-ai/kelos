package controller

import "github.com/kelos-dev/kelos/internal/capture"

// ParseOutputs extracts output lines from log data between the
// ---KELOS_OUTPUTS_START--- and ---KELOS_OUTPUTS_END--- markers.
func ParseOutputs(logData string) []string {
	return capture.ParseOutputs(logData)
}

// ResultsFromOutputs builds a key-value map from output lines in "key: value" format.
func ResultsFromOutputs(outputs []string) map[string]string {
	return capture.ResultsFromOutputs(outputs)
}
