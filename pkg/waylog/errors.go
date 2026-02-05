package waylog

import "errors"

// Sentinel errors for the waylog package.
var (
	// ErrAlreadyInitialized is returned when Init is called more than once.
	ErrAlreadyInitialized = errors.New("waylog: already initialized")

	// ErrServiceRequired is returned when Config.Service is empty.
	ErrServiceRequired = errors.New("waylog: service is required")

	// ErrEnvRequired is returned when Config.Env is empty.
	ErrEnvRequired = errors.New("waylog: env is required")
)
