package causal

import (
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

const (
	minAfterFailures  = 30
	maxBeforeFailures = 5
	minLift           = 3.0
	maxDeployGap      = 30 * time.Minute
)

// InferIntroducedBy scans the graph for error spikes that follow a deployment
// and returns causal claims linking error codes to the deploy that likely
// introduced them. All returned claims have ShadowMode=true.
//
// deps must have their FirstSeen within [start, end]; callers should pre-filter
// using coldstore.DeploymentsInWindow and convert to []DeploymentInfo.
//
// A claim is emitted when:
//   - Exactly one deployment for the service falls inside [start, end].
//   - After-failures >= minAfterFailures and before-failures <= maxBeforeFailures.
//   - The first post-deploy failure occurs within maxDeployGap of the deploy.
//   - Lift (after / before, Laplace-smoothed) >= minLift.
func InferIntroducedBy(g *core.Graph, deps []DeploymentInfo, start, end time.Time) []Claim {
	// Bucket deployments by service, filtered to the analysis window.
	svcDeploys := map[string][]DeploymentInfo{}
	for _, d := range deps {
		if d.FirstSeen.Before(start) || d.FirstSeen.After(end) {
			continue
		}
		svcDeploys[d.Service] = append(svcDeploys[d.Service], d)
	}

	// Keep only services with exactly one deployment (ambiguous otherwise).
	uniqueDeploys := map[string]DeploymentInfo{}
	for svc, ds := range svcDeploys {
		if len(ds) == 1 {
			uniqueDeploys[svc] = ds[0]
		}
	}

	if len(uniqueDeploys) == 0 {
		return nil
	}

	type failureKey struct {
		service   string
		errorCode string
	}
	type failureCounts struct {
		before       int
		after        int
		firstFailure time.Time
	}
	counts := map[failureKey]*failureCounts{}

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		reqNode, ok := g.Nodes[e.From]
		if !ok || reqNode.Type != core.NodeRequest {
			continue
		}
		errNode, ok := g.Nodes[e.To]
		if !ok || errNode.Type != core.NodeError {
			continue
		}

		if reqNode.LastSeen.Before(start) || reqNode.LastSeen.After(end) {
			continue
		}

		svc := core.ServiceFromNode(reqNode)
		if svc == "" {
			continue
		}

		deploy, ok := uniqueDeploys[svc]
		if !ok {
			continue
		}

		code, _ := errNode.Attr["code"].(string)
		if code == "" {
			continue
		}

		key := failureKey{service: svc, errorCode: code}
		fc, ok := counts[key]
		if !ok {
			fc = &failureCounts{}
			counts[key] = fc
		}

		if reqNode.LastSeen.Before(deploy.FirstSeen) {
			fc.before++
		} else {
			fc.after++
			if fc.firstFailure.IsZero() || reqNode.LastSeen.Before(fc.firstFailure) {
				fc.firstFailure = reqNode.LastSeen
			}
		}
	}

	var claims []Claim
	for key, fc := range counts {
		if fc.after < minAfterFailures {
			continue
		}
		if fc.before > maxBeforeFailures {
			continue
		}

		deploy := uniqueDeploys[key.service]

		if fc.firstFailure.IsZero() || fc.firstFailure.Sub(deploy.FirstSeen) > maxDeployGap {
			continue
		}

		// Laplace smoothing: avoid divide-by-zero; treat zero prior as 0.5.
		beforeRate := float64(fc.before)
		if beforeRate == 0 {
			beforeRate = 0.5
		}
		lift := float64(fc.after) / beforeRate
		if lift < minLift {
			continue
		}

		timeDelta := fc.firstFailure.Sub(deploy.FirstSeen)
		ev := Evidence{
			BeforeFailures: fc.before,
			AfterFailures:  fc.after,
			Lift:           lift,
			TimeDeltaMin:   timeDelta.Minutes(),
			WindowMinutes:  end.Sub(start).Minutes(),
		}

		conf, tier := Score(ev)

		claims = append(claims, Claim{
			ClaimType:   ClaimIntroducedBy,
			Subject:     key.errorCode,
			Target:      deploy.ID,
			Service:     key.service,
			Confidence:  conf,
			Tier:        tier,
			Evidence:    ev,
			WindowStart: start,
			WindowEnd:   end,
			ShadowMode:  true,
		})
	}

	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Service != claims[j].Service {
			return claims[i].Service < claims[j].Service
		}
		return claims[i].Subject < claims[j].Subject
	})

	return claims
}
