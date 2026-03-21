package kafka

// Config holds Kafka transport configuration.
type Config struct {
	Brokers      []string
	Topic        string
	BatchBytes   int64
	RequiredAcks int
	Compression  string
}
