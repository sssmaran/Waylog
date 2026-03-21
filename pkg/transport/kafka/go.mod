module github.com/sssmaran/WaylogCLI/pkg/transport/kafka

go 1.24.2

require (
	github.com/segmentio/kafka-go v0.4.50
	github.com/sssmaran/WaylogCLI/pkg v0.0.0
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
)

replace github.com/sssmaran/WaylogCLI/pkg => ../../
