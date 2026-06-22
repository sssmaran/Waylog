# Writing a Waylog triage brief

A brief is the durable artifact a triage produces: a cited, reproducible summary an
on-call engineer can act on — and that a *later* reader (or agent) can trust without
redoing the work. It is decision-support, not a decision.

## Principles

- **Every claim is tool-derived and cited.** Tie statements to a `report_hash`,
  `evidence_fingerprint`, and the specific trace / alert / signal / deploy ids.
  If a tool didn't say it, the brief doesn't claim it.
- **The engineer decides.** State a *recommended* next action and the evidence for
  it; never present it as settled or as something already done.
- **Reproducible.** Anyone re-running the same tools on the same incident state gets
  the same `report_hash`. Quote it so the brief is self-verifying.
- **Honest about uncertainty.** Carry the engine's `confidence` through verbatim.
  If `cause=unknown` or evidence is thin, say so and say what would resolve it —
  don't manufacture a root cause.

## Template

```markdown
> *AI-assisted triage — evidence is tool-derived and reproducible; an on-call engineer owns the decision.*

## Incident <incident_id> — <service>/<step>/<error_code>

**Status / cause / confidence:** active · deploy · high
**Impact:** <N> users · <N> requests · <N> services
**report_hash:** sha256:…   **evidence_fingerprint:** sha256:…

**Leading hypothesis (what changed):**
- Deploy <service> <version> (PR <pr_url>, by <author>, commit <sha>); error rate
  <before>% → <after>% after deploy.  [from suspect_change]
- — or — No deploy correlated; first failure is <step>/<error_code> at <trace_id>.

**Evidence:**
- First failure: trace `<trace_id>` — <one-line story from explain_request>.
- Blast: <N> requests / <N> users / <N> services [from blast_radius].
- Signals/alerts/runtime cited by the report: <ids>.

**Recommended next action (engineer decides):**
- <e.g. review/revert PR <pr_url>; verify <downstream> health; roll back <version>>.

**If confidence is low — what's missing:**
- <instrumentation_warnings + the concrete telemetry/registration that would make
  the next occurrence diagnosable>.
```

## ready-for-human vs. ready-for-agent

If a downstream AFK agent could safely execute the recommended next *investigation*
(read-only deepening — more traces, wider blast), say so. If the next step needs
human judgment, external access, or a write-side mitigation (revert, rollback,
config change), mark it **engineer-only** and say why. Waylog never executes
mitigations; those are always the engineer's.
