package monitor

import (
	"context"
	"sort"
	"sync"

	"xkeen-panel/internal/models"
	"xkeen-panel/internal/xkeen"
)

type verifiedCandidate struct {
	server   models.Server
	outbound map[string]interface{}
}

type verifiedResult struct {
	index  int
	result xkeen.ProbeResult
	err    error
}

func (w *Watchdog) verifiedCandidates(current map[string]interface{}, higherThan *models.Server, automatic bool) []verifiedCandidate {
	var candidates []verifiedCandidate
	for _, server := range xkeen.PolicyCandidates(w.subscription.GetServers(), w.config.VerifiedFailover) {
		if higherThan != nil && !xkeen.PolicyBetter(server, *higherThan, w.config.VerifiedFailover) {
			continue
		}
		// A periodic return must recheck a formerly blocked priority even if
		// its temporary outage blacklist has not yet expired.
		if automatic && higherThan == nil && w.isBlacklisted(server.RawURI) {
			continue
		}
		outbound, err := xkeen.OutboundForServer(current, &server)
		if err != nil {
			continue
		}
		if automatic && xkeen.SameOutbound(outbound, current) && w.verifiedApplier.Running() {
			continue
		}
		candidates = append(candidates, verifiedCandidate{server: server, outbound: outbound})
	}
	// A flattened Auto profile acts as one logical choice. If its active node
	// fails, try siblings before falling back to the global subscription policy.
	// Periodic return-to-priority uses higherThan and keeps the global order.
	if automatic && higherThan == nil && w.verifiedCurrent != nil {
		group := xkeen.NumberedGroupName(w.verifiedCurrent.Name)
		source := w.verifiedCurrent.SourceID
		if group != "" {
			sort.SliceStable(candidates, func(i, j int) bool {
				inGroup := func(candidate verifiedCandidate) bool {
					return candidate.server.SourceID == source && xkeen.NumberedGroupName(candidate.server.Name) == group
				}
				return inGroup(candidates[i]) && !inGroup(candidates[j])
			})
		}
	}
	return candidates
}

// A bounded worker pool probes connections only; the caller alone applies a
// config. Automatic selection consumes results in policy order, while the UI
// receives ping results as they finish. Returning false cancels/reaps all work.
func (w *Watchdog) probeVerifiedCandidates(parent context.Context, candidates []verifiedCandidate, ordered bool, visit func(int, xkeen.ProbeResult, error) bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	workers := w.config.ProbeConcurrency
	if workers <= 0 || workers > xkeen.VerifiedProbeConcurrency {
		workers = xkeen.VerifiedProbeConcurrency
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}
	jobs := make(chan int)
	results := make(chan verifiedResult, workers)
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		for i := range candidates {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	for range workers {
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				r, err := w.verifiedProbe(ctx, candidates[i].outbound)
				select {
				case results <- verifiedResult{i, r, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	defer func() { cancel(); wg.Wait() }()
	pending := make(map[int]verifiedResult)
	next := 0
	for received := 0; received < len(candidates); received++ {
		var result verifiedResult
		select {
		case result = <-results:
		case <-ctx.Done():
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !ordered {
			if !visit(result.index, result.result, result.err) {
				return nil
			}
			continue
		}
		pending[result.index] = result
		for {
			r, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			if !visit(next, r.result, r.err) {
				return nil
			}
			next++
		}
	}
	return parent.Err()
}
