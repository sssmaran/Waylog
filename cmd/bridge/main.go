package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

func main() {
	brokers := splitEnvList("KAFKA_BROKERS")
	if len(brokers) == 0 {
		brokers = []string{"localhost:9092"}
	}
	topic := getenv("KAFKA_TOPIC", "wide_events")
	ingestURL := getenv("INGEST_URL", "http://localhost:8080/v1/events")
	groupID := getenv("KAFKA_GROUP_ID", "waylog-demo-bridge")

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	})
	defer reader.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	ensureTopicReady(brokers, topic)

	log.Printf("bridge reading %s from %v and posting to %s", topic, brokers, ingestURL)
	for {
		msg, err := reader.ReadMessage(context.Background())
		if err != nil {
			log.Printf("kafka read failed: %v (retrying)", err)
			time.Sleep(2 * time.Second)
			continue
		}

		req, err := http.NewRequest(http.MethodPost, ingestURL, bytes.NewReader(msg.Value))
		if err != nil {
			log.Printf("request build failed: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
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

func getenv(key, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}

func splitEnvList(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func ensureTopicReady(brokers []string, topic string) {
	if len(brokers) == 0 || topic == "" {
		return
	}

	for {
		controller, err := controllerAddress(brokers[0])
		if err != nil {
			log.Printf("kafka controller not ready: %v (retrying)", err)
			time.Sleep(2 * time.Second)
			continue
		}

		conn, err := kafka.Dial("tcp", controller)
		if err != nil {
			log.Printf("kafka controller dial failed: %v (retrying)", err)
			time.Sleep(2 * time.Second)
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
			time.Sleep(2 * time.Second)
			continue
		}

		return
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
