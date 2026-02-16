package tools

const graphStatsInputSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const graphStatsOutputSchema = `{
  "type": "object",
  "properties": {
    "nodes": { "type": "integer" },
    "edges": { "type": "integer" },
    "requests": { "type": "integer" },
    "users": { "type": "integer" },
    "services": { "type": "integer" },
    "feature_flags": { "type": "integer" },
    "failures": { "type": "integer" }
  },
  "required": ["nodes", "edges", "requests", "users", "services", "feature_flags", "failures"],
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
    "request_id": { "type": "string" },
    "latency_ms": {},
    "flow": {},
    "user_id": { "type": "string" },
    "user_tier": {},
    "feature_flags": { "type": "array", "items": { "type": "string" } },
    "span_id": { "type": "string" },
    "span_service": {},
    "span_depth": { "type": "string" },
    "service": {},
    "error_code": {},
    "error_msg": {}
  },
  "required": ["request_id"],
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
    "trace_id": { "type": "string" },
    "roots": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "span_id": { "type": "string" },
          "service": {},
          "children": { "type": "array" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["trace_id", "roots"],
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
    "trace_id": { "type": "string" },
    "request_id": { "type": "string" },
    "event_name": { "type": "string" },
    "flow": { "type": "string" },
    "latency_ms": {},
    "root_span_ids": { "type": "array", "items": { "type": "string" } },
    "paths": {
      "type": "array",
      "items": { "type": "array", "items": { "type": "string" } }
    }
  },
  "required": ["trace_id", "request_id"],
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
    "failures": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "request_id": { "type": "string" },
          "trace_id": { "type": "string" },
          "latency_ms": {},
          "tier": { "type": "string" },
          "error_code": { "type": "string" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["failures"],
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
    "patterns": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "error_code": { "type": "string" },
          "flow": { "type": "string" },
          "user_tier": { "type": "string" },
          "feature_flags": { "type": "array", "items": { "type": "string" } },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["patterns"],
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
    "error_code": { "type": "string" },
    "affected_requests": { "type": "integer" },
    "affected_users": { "type": "integer" },
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
  "required": ["error_code", "affected_requests", "affected_users"],
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
    "request_id": { "type": "string" },
    "services": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["request_id", "services"],
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
    "matched_requests": { "type": "integer" }
  },
  "required": ["matched_requests"],
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

const diffOutputSchema = `{
  "type": "object",
  "properties": {
    "new": { "type": "array" },
    "removed": { "type": "array" },
    "increased": { "type": "array" },
    "decreased": { "type": "array" }
  },
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
  "required": ["total_failures"],
  "additionalProperties": false
}`
