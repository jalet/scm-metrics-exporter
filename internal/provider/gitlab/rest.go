package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	zlog "github.com/rs/zerolog/log"
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

// collectREST dispatches to the group or user projects/merge-requests collector.
func (p *Provider) collectREST(ctx context.Context, target string) (restResult, error) {
	if p.targetType == targetUser {
		return p.collectRESTForUser(ctx, target)
	}
	return p.collectRESTForGroup(ctx, target)
}

// collectRESTForGroup lists the group's projects (including subgroups) for the
// denominator, then sweeps the group's open merge requests once and tallies them by
// project_id. Any HTTP or transport error returns whatever was collected plus the error,
// so the caller records a rest SourceError and still emits GraphQL findings.
func (p *Provider) collectRESTForGroup(ctx context.Context, group string) (restResult, error) {
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
		zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("group", group).Str("phase", "projects").
			Int("page", page).Int("in_page", len(projects)).Int("total", len(res.projects)).Msg("fetched projects page")
		next = nextPage
	}
	zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("group", group).
		Int("projects", len(res.projects)).Msg("gitlab project listing complete")

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
		zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("group", group).Str("phase", "merge_requests").
			Int("page", page).Int("in_page", len(mrs)).Msg("fetched merge requests page")
		next = nextPage
	}
	for i := range res.projects {
		res.projects[i].openMRs = counts[res.projects[i].id] // absent -> 0
	}
	zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("group", group).
		Int("projects_with_open_mrs", len(counts)).Msg("gitlab merge request sweep complete")
	return res, nil
}

// collectRESTForUser lists the user's projects for the denominator, then counts open merge
// requests per project. GitLab has no user-wide MR sweep endpoint, so this is an N+1 call
// (one MR query per project). Any HTTP or transport error returns what was collected plus
// the error.
func (p *Provider) collectRESTForUser(ctx context.Context, user string) (restResult, error) {
	var res restResult
	uid := escapeGroup(user)

	projPath := fmt.Sprintf("/users/%s/projects?simple=true&per_page=100", uid)
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
		zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("user", user).Str("phase", "projects").
			Int("page", page).Int("in_page", len(projects)).Int("total", len(res.projects)).Msg("fetched user projects page")
		next = nextPage
	}

	for i := range res.projects {
		count, rate, ok, err := p.countProjectOpenMRs(ctx, res.projects[i].id)
		if ok {
			res.rate, res.rateKnown = rate, true
		}
		if err != nil {
			return res, err
		}
		res.projects[i].openMRs = count
	}
	zlog.Debug().Str("provider", "gitlab").Str("source", "rest").Str("user", user).
		Int("projects", len(res.projects)).Msg("gitlab user projects + per-project MR counts complete")
	return res, nil
}

// countProjectOpenMRs tallies a single project's open merge requests across pages.
func (p *Provider) countProjectOpenMRs(ctx context.Context, projectID int64) (count int, rate int64, rateKnown bool, err error) {
	path := fmt.Sprintf("/projects/%d/merge_requests?state=opened&per_page=100", projectID)
	for page, next := 0, "1"; next != "" && page < p.maxPages; page++ {
		body, nextPage, r, ok, e := p.rest.getPage(ctx, path+"&page="+next)
		if ok {
			rate, rateKnown = r, true
		}
		if e != nil {
			return count, rate, rateKnown, e
		}
		var mrs []json.RawMessage
		if e := json.Unmarshal(body, &mrs); e != nil {
			return count, rate, rateKnown, fmt.Errorf("gitlab rest: decode merge requests: %w", e)
		}
		count += len(mrs)
		next = nextPage
	}
	return count, rate, rateKnown, nil
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
