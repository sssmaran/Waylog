package agentobs

import "fmt"

// DeliveryError reports a batch-level transport failure.
type DeliveryError struct {
	Err error
}

func (e DeliveryError) Error() string {
	return fmt.Sprintf("delivery failed: %v", e.Err)
}

type ClientStats struct {
	EventsSent      int64
	Dropped         int64
	TransportErrors int64
}
