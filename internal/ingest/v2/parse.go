package ingestv2

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	maxBodyBytes  = 1 << 20
	maxBatchItems = 256
)

type parsedRequest struct {
	events [][]byte
}

type requestError struct {
	status int
	reason string
	detail string
}

func parseRequestBody(w http.ResponseWriter, r *http.Request) (parsedRequest, *requestError) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	reader, cleanup, reqErr := requestBodyReader(r)
	if cleanup != nil {
		defer cleanup()
	}
	if reqErr != nil {
		return parsedRequest{}, reqErr
	}

	body, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return parsedRequest{}, &requestError{status: http.StatusRequestEntityTooLarge, reason: ReasonBodyOversize, detail: "request body exceeds 1 MB"}
		}
		return parsedRequest{}, &requestError{status: http.StatusBadRequest, reason: ReasonInvalidBody, detail: err.Error()}
	}
	if len(body) > maxBodyBytes {
		return parsedRequest{}, &requestError{status: http.StatusRequestEntityTooLarge, reason: ReasonBodyOversize, detail: "request body exceeds 1 MB"}
	}

	mediaType, reqErr := requestMediaType(r)
	if reqErr != nil {
		return parsedRequest{}, reqErr
	}
	switch mediaType {
	case "application/json":
		return parsedRequest{events: [][]byte{body}}, nil
	case "application/x-ndjson":
		return parseNDJSON(body)
	default:
		return parsedRequest{}, &requestError{status: http.StatusUnsupportedMediaType, reason: ReasonUnsupportedContentType, detail: "unsupported content type " + mediaType}
	}
}

func requestBodyReader(r *http.Request) (io.Reader, func(), *requestError) {
	encoding := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return r.Body, nil, nil
	}
	if !strings.EqualFold(encoding, "gzip") {
		return nil, nil, &requestError{status: http.StatusBadRequest, reason: ReasonUnsupportedEncoding, detail: "unsupported content encoding " + encoding}
	}
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		return nil, nil, &requestError{status: http.StatusBadRequest, reason: ReasonInvalidBody, detail: err.Error()}
	}
	return gz, func() { _ = gz.Close() }, nil
}

func requestMediaType(r *http.Request) (string, *requestError) {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return "", &requestError{status: http.StatusBadRequest, reason: ReasonInvalidBody, detail: "missing content type"}
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", &requestError{status: http.StatusBadRequest, reason: ReasonInvalidBody, detail: err.Error()}
	}
	return strings.ToLower(mediaType), nil
}

func parseNDJSON(body []byte) (parsedRequest, *requestError) {
	events := make([][]byte, 0, maxBatchItems)
	for len(body) > 0 {
		line := body
		if idx := bytes.IndexByte(body, '\n'); idx >= 0 {
			line = body[:idx]
			body = body[idx+1:]
		} else {
			body = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if len(events) == maxBatchItems {
			return parsedRequest{}, &requestError{status: http.StatusRequestEntityTooLarge, reason: ReasonBatchOversize, detail: "batch exceeds 256 events"}
		}
		events = append(events, line)
	}
	return parsedRequest{events: events}, nil
}
