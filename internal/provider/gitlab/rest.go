package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

type restClient struct {
	httpClient      *http.Client
	apiBase         string
	includeArchived bool
}

type restProject struct {
	id                int64
	pathWithNamespace string // == GraphQL fullPath; the merge key
	openMRs           int
}

type restResult struct {
	projects  []restProject
	rate      int64
	rateKnown bool
}

// collectREST lists the group's projects (including subgroups) for the denominator,
// then sweeps the group's open merge requests once and tallies them by project_id.
// Any HTTP or transport error returns whatever was collected plus the error, so the
// caller records a rest SourceError and still emits GraphQL findings.
func (p *Provider) collectREST(ctx context.Context, group string) (restResult, error) {
	var res restResult
	gid := escapeGroup(group)

	projPath := fmt.Sprintf("/groups/%s/projects?include_subgroups=true&simple=true&per_page=100", gid)
	if !p.rest.includeArchived {
		projPath += "&archived=false"
	}
	for page, next := 0, "1"; next != "" && page < p.maxPages; page++ {
		body, nextPage, rate, ok, err := p.rest.getPage(ctx, projPath+"&page="+next)
		if ok {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		var projects []struct {
			ID                int64  `json:"id"`
			PathWithNamespace string `json:"path_with_namespace"`
		}
		if err := json.Unmarshal(body, &projects); err != nil {
			return res, fmt.Errorf("gitlab rest: decode projects: %w", err)
		}
		for _, pr := range projects {
			res.projects = append(res.projects, restProject{id: pr.ID, pathWithNamespace: pr.PathWithNamespace})
		}
		next = nextPage
	}

	counts := make(map[int64]int)
	mrPath := fmt.Sprintf("/groups/%s/merge_requests?state=opened&scope=all&per_page=100", gid)
	for page, next := 0, "1"; next != "" && page < p.maxPages; page++ {
		body, nextPage, rate, ok, err := p.rest.getPage(ctx, mrPath+"&page="+next)
		if ok {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		var mrs []struct {
			ProjectID int64 `json:"project_id"`
		}
		if err := json.Unmarshal(body, &mrs); err != nil {
			return res, fmt.Errorf("gitlab rest: decode merge requests: %w", err)
		}
		for _, mr := range mrs {
			counts[mr.ProjectID]++
		}
		next = nextPage
	}
	for i := range res.projects {
		res.projects[i].openMRs = counts[res.projects[i].id] // absent -> 0
	}
	return res, nil
}

// getPage GETs one page and returns the body, the X-Next-Page value ("" on the last
// page), and the RateLimit-Remaining reading (rateKnown=false when the header is
// absent, which GitLab sometimes omits). A 429 or other non-200 is an error.
func (c *restClient) getPage(ctx context.Context, path string) (body []byte, nextPage string, rate int64, rateKnown bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+path, nil)
	if err != nil {
		return nil, "", 0, false, fmt.Errorf("gitlab rest: new request: %w", err)
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // apiBase is operator-configured (BaseURL), not attacker input
	if err != nil {
		return nil, "", 0, false, fmt.Errorf("gitlab rest: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if v := resp.Header.Get("RateLimit-Remaining"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			rate, rateKnown = n, true
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", rate, rateKnown, fmt.Errorf("gitlab rest: 429 throttled: retry-after=%s", resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", rate, rateKnown, fmt.Errorf("gitlab rest: http %d for %s", resp.StatusCode, path)
	}
	b, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return nil, "", rate, rateKnown, fmt.Errorf("gitlab rest: read response: %w", rerr)
	}
	return b, resp.Header.Get("X-Next-Page"), rate, rateKnown, nil
}
