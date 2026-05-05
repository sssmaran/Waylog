package incidents

func NextChecks(cause Cause, confidence Confidence) []string {
	switch cause {
	case CauseDeploy:
		return []string{
			"Compare error onset with the deployment timestamp.",
			"Check whether the deployed service version appears on failing traces.",
			"Roll back or canary-disable the deployment if the affected family is still rising.",
		}
	case CauseDependency:
		return []string{
			"Check the downstream service health and recent deploys.",
			"Inspect retries, timeouts, and circuit-breaker state for the failing step.",
			"Notify the downstream owner with sample traces and affected service list.",
		}
	case CauseApp:
		return []string{
			"Inspect the first failing step and recent application logs.",
			"Compare failing request fields against recent successful requests.",
			"Add instrumentation if the step lacks enough context to isolate the bad branch.",
		}
	default:
		return []string{
			"Inspect sample traces for missing downstream or deploy evidence.",
			"Check whether production signals are being posted to /v1/signals.",
			"Add service version and dependency health signals to improve classification.",
		}
	}
}
