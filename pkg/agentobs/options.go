package agentobs

import "time"

type ClientOption func(*clientConfig)
type SessionOption func(*sessionConfig)

type clientConfig struct {
	apiKey            string
	flushInterval     time.Duration
	batchSize         int
	queueSize         int
	heartbeatInterval time.Duration
	maxRetries        int
	errorHandler      func(DeliveryError)
}

type sessionConfig struct {
	version string
	prompt  string
}

func WithAPIKey(key string) ClientOption                    { return func(c *clientConfig) { c.apiKey = key } }
func WithFlushInterval(d time.Duration) ClientOption        { return func(c *clientConfig) { c.flushInterval = d } }
func WithBatchSize(n int) ClientOption                      { return func(c *clientConfig) { c.batchSize = n } }
func WithQueueSize(n int) ClientOption                      { return func(c *clientConfig) { c.queueSize = n } }
func WithHeartbeatInterval(d time.Duration) ClientOption    { return func(c *clientConfig) { c.heartbeatInterval = d } }
func WithRetry(n int) ClientOption                          { return func(c *clientConfig) { c.maxRetries = n } }
func WithErrorHandler(fn func(DeliveryError)) ClientOption  { return func(c *clientConfig) { c.errorHandler = fn } }
func WithVersion(v string) SessionOption                    { return func(c *sessionConfig) { c.version = v } }
func WithPrompt(p string) SessionOption                     { return func(c *sessionConfig) { c.prompt = p } }
