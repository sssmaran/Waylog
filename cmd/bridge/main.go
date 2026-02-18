package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/sssmaran/WaylogCLI/internal/config"
)

func main() {
	level := parseSlogLevel(config.Getenv("LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	brokers := config.SplitEnvList("KAFKA_BROKERS")
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	topic := config.Getenv("KAFKA_TOPIC", "wide_events")
	ingestURL := config.Getenv("INGEST_URL", "http://localhost:8080/v1/events")
	groupID := config.Getenv("KAFKA_GROUP_ID", "waylog-demo-bridge")
	apiKey := config.Getenv("WAYLOG_API_KEY", "")
	dlqDir := config.Getenv("DLQ_DIR", "./data")

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	})
	defer reader.Close()

	dlq := newDeadLetterWriter(dlqDir)
	defer dlq.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	if err := ensureTopicReady(ctx, brokers, topic); err != nil {
		if ctx.Err() != nil {
			slog.Info("bridge shutdown during startup")
			return
		}
		slog.Error("failed to ensure topic ready", "err", err)
		os.Exit(1)
	}

	slog.Info("bridge started", "topic", topic, "brokers", brokers, "ingest_url", ingestURL)
	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("bridge shutdown signal received")
				return
			}
			slog.Warn("kafka fetch failed, retrying", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL, bytes.NewReader(msg.Value))
		if err != nil {
			slog.Error("request build failed", "err", err, "offset", msg.Offset)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("bridge shutdown signal received")
				return
			}
			// Transport error — do not commit, retry on restart
			slog.Warn("ingest post failed, will retry on restart", "err", err, "offset", msg.Offset)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		status := resp.StatusCode

		switch {
		// 2xx — success, commit
		case status >= 200 && status < 300:
			commitWithTimeout(reader, msg)

		// 429 — backpressure, do not commit (retry later)
		case status == http.StatusTooManyRequests:
			slog.Warn("ingest returned 429, backing off", "offset", msg.Offset)
			time.Sleep(2 * time.Second)

		// 5xx — transient server error, do not commit
		case status >= 500:
			slog.Warn("ingest returned 5xx, skipping commit for retry", "status", status, "offset", msg.Offset)

		// 4xx (except 429) — poison payload, persist to DLQ then commit
		default:
			slog.Error("ingest rejected message, writing to dead letter",
				"status", status, "offset", msg.Offset, "partition", msg.Partition)
			if err := dlq.Write(deadLetterEntry{
				Topic:        msg.Topic,
				Partition:    msg.Partition,
				Offset:       msg.Offset,
				Key:          string(msg.Key),
				StatusCode:   status,
				ResponseBody: string(respBody),
				Payload:      string(msg.Value),
				Timestamp:    time.Now().UTC(),
			}); err != nil {
				// DLQ write failed — do NOT commit to avoid silent message loss
				slog.Error("dead letter write failed, skipping commit", "err", err, "offset", msg.Offset)
				continue
			}
			commitWithTimeout(reader, msg)
		}
	}
}

// commitWithTimeout commits the message with a dedicated 3s timeout,
// independent of the request context.
func commitWithTimeout(reader *kafka.Reader, msg kafka.Message) {
	commitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := reader.CommitMessages(commitCtx, msg); err != nil {
		slog.Warn("kafka commit failed, duplicate delivery possible", "err", err, "offset", msg.Offset)
	}
}

// --- Dead letter writer ---

type deadLetterEntry struct {
	Topic        string    `json:"topic"`
	Partition    int       `json:"partition"`
	Offset       int64     `json:"offset"`
	Key          string    `json:"key"`
	StatusCode   int       `json:"status_code"`
	ResponseBody string    `json:"response_body"`
	Payload      string    `json:"payload"`
	Timestamp    time.Time `json:"timestamp"`
}

type deadLetterWriter struct {
	mu   sync.Mutex
	file *os.File
}

func newDeadLetterWriter(dir string) *deadLetterWriter {
	os.MkdirAll(dir, 0o755)
	name := fmt.Sprintf("%s/deadletter-%s.jsonl", dir, time.Now().UTC().Format("20060102"))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("failed to open dead letter file", "err", err, "path", name)
		return &deadLetterWriter{}
	}
	slog.Info("dead letter file opened", "path", name)
	return &deadLetterWriter{file: f}
}

func (d *deadLetterWriter) Write(entry deadLetterEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return fmt.Errorf("dead letter file not open")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = d.file.Write(data)
	if err != nil {
		return err
	}
	return d.file.Sync()
}

func (d *deadLetterWriter) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		d.file.Close()
	}
}

// --- Helpers ---

func parseSlogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func ensureTopicReady(ctx context.Context, brokers []string, topic string) error {
	if len(brokers) == 0 || topic == "" {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		controller, err := controllerAddress(brokers[0])
		if err != nil {
			slog.Warn("kafka controller not ready, retrying", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}

		conn, err := kafka.Dial("tcp", controller)
		if err != nil {
			slog.Warn("kafka controller dial failed, retrying", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}

		err = conn.CreateTopics(kafka.TopicConfig{
			Topic:             topic,
			NumPartitions:     1,
			ReplicationFactor: 1,
		})
		_ = conn.Close()

		if err != nil && !strings.Contains(err.Error(), "Topic with this name already exists") {
			slog.Warn("kafka create topic failed, retrying", "err", err, "topic", topic)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}

		return nil
	}
}

func controllerAddress(broker string) (string, error) {
	conn, err := kafka.Dial("tcp", broker)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)), nil
}
