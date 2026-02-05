package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/sssmaran/WaylogCLI/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	brokers := config.SplitEnvList("KAFKA_BROKERS")
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	topic := config.Getenv("KAFKA_TOPIC", "wide_events")
	ingestURL := config.Getenv("INGEST_URL", "http://localhost:8080/v1/events")
	groupID := config.Getenv("KAFKA_GROUP_ID", "waylog-demo-bridge")

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	})
	defer reader.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	if err := ensureTopicReady(ctx, brokers, topic); err != nil {
		if ctx.Err() != nil {
			log.Println("bridge shutdown during startup")
			return
		}
		log.Fatalf("failed to ensure topic ready: %v", err)
	}

	log.Printf("bridge reading %s from %v and posting to %s", topic, brokers, ingestURL)
	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("bridge shutdown signal received")
				return
			}
			log.Printf("kafka read failed: %v (retrying)", err)
			time.Sleep(2 * time.Second)
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ingestURL, bytes.NewReader(msg.Value))
		if err != nil {
			log.Printf("request build failed: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("bridge shutdown signal received")
				return
			}
			log.Printf("ingest post failed: %v", err)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("ingest returned status %d", resp.StatusCode)
		}
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
			log.Printf("kafka controller not ready: %v (retrying)", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}

		conn, err := kafka.Dial("tcp", controller)
		if err != nil {
			log.Printf("kafka controller dial failed: %v (retrying)", err)
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
			log.Printf("kafka create topic failed: %v (retrying)", err)
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
