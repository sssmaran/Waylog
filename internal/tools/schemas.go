package tools

const graphStatsInputSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const graphStatsOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "nodes": { "type": "integer" },
    "edges": { "type": "integer" },
    "requests": { "type": "integer" },
    "users": { "type": "integer" },
    "services": { "type": "integer" },
    "feature_flags": { "type": "integer" },
    "failures": { "type": "integer" }
  },
  "required": ["schema_version", "nodes", "edges", "requests", "users", "services", "feature_flags", "failures"],
  "additionalProperties": false
}`

const explainRequestInputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" },
    "trace_id": { "type": "string" }
  },
  "additionalProperties": false
}`

const explainRequestOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "request_id": { "type": "string" },
    "latency_ms": { "type": ["number", "null"] },
    "flow": { "type": ["string", "null"] },
    "user_id": { "type": "string" },
    "user_tier": { "type": ["string", "null"] },
    "feature_flags": { "type": "array", "items": { "type": "string" } },
    "span_id": { "type": "string" },
    "span_service": { "type": ["string", "null"] },
    "span_depth": { "type": "string" },
    "service": { "type": ["string", "null"] },
    "error_code": { "type": ["string", "null"] },
    "error_msg": { "type": ["string", "null"] },
    "span_chain": { "type": "array", "items": { "type": "object" } }
  },
  "required": ["schema_version", "request_id"],
  "additionalProperties": false
}`

const traceGraphInputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" }
  },
  "required": ["trace_id"],
  "additionalProperties": false
}`

const traceGraphOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "trace_id": { "type": "string" },
    "roots": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "span_id": { "type": "string" },
          "service": { "type": ["string", "null"] },
          "children": { "type": "array" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["schema_version", "trace_id", "roots"],
  "additionalProperties": false
}`

const traceSummaryInputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" }
  },
  "required": ["trace_id"],
  "additionalProperties": false
}`

const traceSummaryOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "trace_id": { "type": "string" },
    "request_id": { "type": "string" },
    "event_name": { "type": "string" },
    "flow": { "type": "string" },
    "latency_ms": { "type": ["number", "null"] },
    "root_span_ids": { "type": "array", "items": { "type": "string" } },
    "paths": {
      "type": "array",
      "items": { "type": "array", "items": { "type": "string" } }
    }
  },
  "required": ["schema_version", "trace_id", "request_id"],
  "additionalProperties": false
}`

const failuresInputSchema = `{
  "type": "object",
  "properties": {
    "tier": { "type": "string" }
  },
  "additionalProperties": false
}`

const failuresOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "failures": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "request_id": { "type": "string" },
          "trace_id": { "type": "string" },
          "latency_ms": { "type": ["number", "null"] },
          "tier": { "type": "string" },
          "error_code": { "type": "string" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["schema_version", "failures"],
  "additionalProperties": false
}`

const patternsInputSchema = `{
  "type": "object",
  "properties": {
    "window": { "type": "string" }
  },
  "additionalProperties": false
}`

const patternsOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "patterns": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "error_code": { "type": "string" },
          "flow": { "type": "string" },
          "user_tier": { "type": "string" },
          "feature_flags": { "type": ["array", "null"], "items": { "type": "string" } },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["schema_version", "patterns"],
  "additionalProperties": false
}`

const blastInputSchema = `{
  "type": "object",
  "properties": {
    "error_code": { "type": "string" },
    "include_services": { "type": "boolean" },
    "top_users": { "type": "integer" },
    "by_tier": { "type": "boolean" }
  },
  "required": ["error_code"],
  "additionalProperties": false
}`

const blastOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "error_code": { "type": "string" },
    "affected_requests": { "type": "integer" },
    "affected_users": { "type": "integer" },
    "vip_users": { "type": "integer" },
    "severity_score": { "type": "number" },
    "services": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "service": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "tiers": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "tier": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "top_users": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "user_id": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "feature_flags": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["schema_version", "error_code", "affected_requests", "affected_users", "vip_users", "severity_score"],
  "additionalProperties": false
}`

const chainInputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" }
  },
  "required": ["request_id"],
  "additionalProperties": false
}`

const chainOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "request_id": { "type": "string" },
    "services": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["schema_version", "request_id", "services"],
  "additionalProperties": false
}`

const queryInputSchema = `{
  "type": "object",
  "properties": {
    "expr": { "type": "string" },
    "window": { "type": "string" }
  },
  "required": ["expr", "window"],
  "additionalProperties": false
}`

const queryOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "matched_requests": { "type": "integer" }
  },
  "required": ["schema_version", "matched_requests"],
  "additionalProperties": false
}`

const diffInputSchema = `{
  "type": "object",
  "properties": {
    "current": { "type": "string" },
    "baseline": { "type": "string" },
    "offset": { "type": "string" }
  },
  "required": ["current", "baseline", "offset"],
  "additionalProperties": false
}`

const diffEntryItemSchema = `{
  "type": "object",
  "properties": {
    "error_code": { "type": "string" },
    "before": { "type": "integer" },
    "after": { "type": "integer" },
    "delta": { "type": "integer" }
  },
  "additionalProperties": false
}`

const diffOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "new": { "type": "array", "items": ` + diffEntryItemSchema + ` },
    "removed": { "type": "array", "items": ` + diffEntryItemSchema + ` },
    "increased": { "type": "array", "items": ` + diffEntryItemSchema + ` },
    "decreased": { "type": "array", "items": ` + diffEntryItemSchema + ` },
    "total_requests_before": { "type": "integer" },
    "total_requests_after": { "type": "integer" },
    "total_failures_before": { "type": "integer" },
    "total_failures_after": { "type": "integer" },
    "latency_p50_before": { "type": "integer" },
    "latency_p50_after": { "type": "integer" },
    "latency_p95_before": { "type": "integer" },
    "latency_p95_after": { "type": "integer" },
    "latency_p99_before": { "type": "integer" },
    "latency_p99_after": { "type": "integer" }
  },
  "required": ["schema_version"],
  "additionalProperties": false
}`

const insightsInputSchema = `{
  "type": "object",
  "properties": {
    "window": { "type": "string" },
    "top_errors": { "type": "integer" },
    "top_services": { "type": "integer" }
  },
  "additionalProperties": false
}`

const insightsOutputSchema = `{
  "type": "object",
  "properties": {
    "schema_version": { "type": "string" },
    "total_failures": { "type": "integer" },
    "top_errors": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "error_code": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "top_services": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "service": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["schema_version", "total_failures"],
  "additionalProperties": false
}`
