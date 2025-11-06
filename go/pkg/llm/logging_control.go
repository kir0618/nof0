package llm

import "sync/atomic"

var verboseLogging atomic.Bool

// SetVerboseLogging toggles whether full prompts/responses are emitted to logs.
func SetVerboseLogging(enabled bool) {
	if enabled {
		verboseLogging.Store(true)
		return
	}
	verboseLogging.Store(false)
}

func isVerboseLoggingEnabled() bool {
	return verboseLogging.Load()
}
