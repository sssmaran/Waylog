package transporthttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func (c *Client) flushBatch(batch []*eventv2.Event) {
	if c.url == "" || len(batch) == 0 {
		return
	}

	for _, chunk := range splitBatches(batch, c.cfg.MaxBatch, c.cfg.MaxBatchSize) {
		var body bytes.Buffer
		enc := json.NewEncoder(&body)
		for _, ev := range chunk {
			if err := enc.Encode(ev); err != nil {
				continue
			}
		}
		req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body.Bytes()))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/x-ndjson")
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func splitBatches(batch []*eventv2.Event, maxCount, maxBytes int) [][]*eventv2.Event {
	if len(batch) == 0 {
		return nil
	}
	if maxCount <= 0 {
		maxCount = len(batch)
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	var out [][]*eventv2.Event
	start := 0
	for start < len(batch) {
		end := start
		size := 0
		for end < len(batch) && end-start < maxCount {
			next := int(estimateEventSize(batch[end]))
			if end > start && size+next > maxBytes {
				break
			}
			size += next
			end++
		}
		if end == start {
			end++
		}
		out = append(out, batch[start:end])
		start = end
	}
	return out
}
