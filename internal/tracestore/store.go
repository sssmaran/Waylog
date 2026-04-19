package tracestore

import (
	"slices"
	"sync"
	"time"
)

type SpanRecord struct {
	SpanID            string
	ParentSpanID      string
	Service           string
	EventName         string
	StatusCode        int
	Success           bool
	LatencyMs         int64
	ErrorCode         string
	ErrorMessage      string
	ErrorPath         string
	ErrorReason       string
	CallerService     string
	DownstreamService string
	Timestamp         time.Time
	HTTPMethod        string
	RouteTemplate     string
	RetryOf           int
	RetryPreviousID   string
	Metadata          map[string]any
}

type TraceRecord struct {
	TraceID   string
	RequestID string
	Spans     []SpanRecord
	UpdatedAt time.Time
}

type Store struct {
	mu              sync.RWMutex
	traces          map[string]*TraceRecord
	traceLastBucket map[string]time.Time
	cohorts         []*cohort
}

type cohort struct {
	bucket   time.Time
	traceIDs map[string]struct{}
}

func NewStore() *Store {
	return &Store{
		traces:          map[string]*TraceRecord{},
		traceLastBucket: map[string]time.Time{},
	}
}

func (s *Store) Get(traceID string) (*TraceRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.traces[traceID]
	if !ok {
		return nil, false
	}
	return cloneTraceRecord(record), true
}

func (s *Store) Upsert(traceID, requestID string, span *SpanRecord) {
	if traceID == "" || span == nil || span.SpanID == "" {
		return
	}

	now := time.Now().UTC()

	// Use the span's event timestamp for cohort bucketing so that replayed
	// and late-arriving events land in the correct time bucket instead of
	// inflating the current cohort.
	ts := span.Timestamp
	if ts.IsZero() {
		ts = now
	}
	bucket := ts.Truncate(time.Minute)

	s.mu.Lock()
	defer s.mu.Unlock()

	record := s.traces[traceID]
	if record == nil {
		record = &TraceRecord{TraceID: traceID}
		s.traces[traceID] = record
	}
	if record.RequestID == "" && requestID != "" {
		record.RequestID = requestID
	}

	merged := false
	for i := range record.Spans {
		if record.Spans[i].SpanID != span.SpanID {
			continue
		}
		mergeSpanRecord(&record.Spans[i], *span)
		merged = true
		break
	}
	if !merged {
		record.Spans = append(record.Spans, *span)
	}

	record.UpdatedAt = now
	s.moveTraceToBucketLocked(traceID, bucket)
}

func (s *Store) ForEachSpan(start, end time.Time, fn func(traceID string, span SpanRecord)) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for traceID, record := range s.traces {
		for _, span := range record.Spans {
			ts := span.Timestamp
			if ts.IsZero() {
				ts = record.UpdatedAt
			}
			if ts.IsZero() || ts.Before(start) || ts.After(end) {
				continue
			}
			fn(traceID, span)
		}
	}
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.traces)
}

func (s *Store) SpanCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, rec := range s.traces {
		n += len(rec.Spans)
	}
	return n
}

func (s *Store) CohortCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cohorts)
}

func (s *Store) PruneOlderThan(cutoff time.Time) (deletedTraces int, deletedCohorts int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := 0
	for idx < len(s.cohorts) {
		c := s.cohorts[idx]
		if !c.bucket.Before(cutoff) {
			break
		}
		for traceID := range c.traceIDs {
			delete(s.traces, traceID)
			delete(s.traceLastBucket, traceID)
			deletedTraces++
		}
		idx++
	}
	if idx > 0 {
		s.cohorts = slices.Delete(s.cohorts, 0, idx)
		deletedCohorts = idx
	}
	return deletedTraces, deletedCohorts
}

func cloneTraceRecord(record *TraceRecord) *TraceRecord {
	if record == nil {
		return nil
	}
	out := &TraceRecord{
		TraceID:   record.TraceID,
		RequestID: record.RequestID,
		UpdatedAt: record.UpdatedAt,
	}
	if len(record.Spans) > 0 {
		out.Spans = append([]SpanRecord(nil), record.Spans...)
	}
	return out
}

func mergeSpanRecord(dst *SpanRecord, src SpanRecord) {
	if dst.ParentSpanID == "" && src.ParentSpanID != "" {
		dst.ParentSpanID = src.ParentSpanID
	}
	if dst.Service == "" && src.Service != "" {
		dst.Service = src.Service
	}
	if dst.EventName == "" && src.EventName != "" {
		dst.EventName = src.EventName
	}
	if dst.StatusCode == 0 && src.StatusCode != 0 {
		dst.StatusCode = src.StatusCode
	}
	if !dst.Success && src.Success {
		dst.Success = src.Success
	}
	if dst.LatencyMs == 0 && src.LatencyMs != 0 {
		dst.LatencyMs = src.LatencyMs
	}
	if dst.ErrorCode == "" && src.ErrorCode != "" {
		dst.ErrorCode = src.ErrorCode
	}
	if dst.ErrorMessage == "" && src.ErrorMessage != "" {
		dst.ErrorMessage = src.ErrorMessage
	}
	if dst.ErrorPath == "" && src.ErrorPath != "" {
		dst.ErrorPath = src.ErrorPath
	}
	if dst.ErrorReason == "" && src.ErrorReason != "" {
		dst.ErrorReason = src.ErrorReason
	}
	if dst.CallerService == "" && src.CallerService != "" {
		dst.CallerService = src.CallerService
	}
	if dst.DownstreamService == "" && src.DownstreamService != "" {
		dst.DownstreamService = src.DownstreamService
	}
	if dst.Timestamp.IsZero() && !src.Timestamp.IsZero() {
		dst.Timestamp = src.Timestamp
	}
	if dst.HTTPMethod == "" && src.HTTPMethod != "" {
		dst.HTTPMethod = src.HTTPMethod
	}
	if dst.RouteTemplate == "" && src.RouteTemplate != "" {
		dst.RouteTemplate = src.RouteTemplate
	}
}

func (s *Store) moveTraceToBucketLocked(traceID string, bucket time.Time) {
	if old, ok := s.traceLastBucket[traceID]; ok && old.Equal(bucket) {
		return
	} else if ok {
		s.removeTraceFromBucketLocked(traceID, old)
	}

	s.traceLastBucket[traceID] = bucket
	cohort := s.cohortForBucketLocked(bucket)
	cohort.traceIDs[traceID] = struct{}{}
}

func (s *Store) removeTraceFromBucketLocked(traceID string, bucket time.Time) {
	for i := range s.cohorts {
		if !s.cohorts[i].bucket.Equal(bucket) {
			continue
		}
		delete(s.cohorts[i].traceIDs, traceID)
		if len(s.cohorts[i].traceIDs) == 0 {
			s.cohorts = slices.Delete(s.cohorts, i, i+1)
		}
		return
	}
}

func (s *Store) cohortForBucketLocked(bucket time.Time) *cohort {
	for i := range s.cohorts {
		if s.cohorts[i].bucket.Equal(bucket) {
			return s.cohorts[i]
		}
		if s.cohorts[i].bucket.After(bucket) {
			c := &cohort{bucket: bucket, traceIDs: map[string]struct{}{}}
			s.cohorts = slices.Insert(s.cohorts, i, c)
			return c
		}
	}
	c := &cohort{bucket: bucket, traceIDs: map[string]struct{}{}}
	s.cohorts = append(s.cohorts, c)
	return c
}
