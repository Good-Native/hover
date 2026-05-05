package jobs

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/good-native/hover/internal/crawler"
	"github.com/good-native/hover/internal/db"
	"golang.org/x/net/publicsuffix"
)

type LinkDiscoveryDeps struct {
	DBQueue     DbQueueInterface
	JobManager  JobManagerInterface
	MinPriority float64
}

// Priority promotion is handled by EnqueueURLs via
// `priority_score = GREATEST(tasks.priority_score, EXCLUDED.priority_score)`,
// and CreatePageRecords' no-op DO UPDATE forces RETURNING to emit pre-existing
// rows too — so no caller-level updateTaskPriorities step is required.
func ProcessDiscoveredLinks(ctx context.Context, deps LinkDiscoveryDeps, task *Task, links map[string][]string, sourceURL string, robotsRules *crawler.RobotsRules) {
	if deps.JobManager == nil {
		jobsLog.Warn("JobManager is nil; skipping link discovery", "task_id", task.ID)
		return
	}

	jobsLog.Debug("Starting link processing and priority assignment",
		"task_id", task.ID,
		"total_links_found", len(links["header"])+len(links["footer"])+len(links["body"]),
		"find_links_enabled", task.FindLinks,
	)

	domainID := task.DomainID
	if domainID == 0 {
		jobsLog.Error("Missing domain ID; skipping link processing", "task_id", task.ID, "job_id", task.JobID)
		return
	}

	isHomepage := task.Path == "/"

	processLinkCategory := func(links []string, priority float64) {
		if len(links) == 0 {
			return
		}
		if priority < deps.MinPriority {
			jobsLog.Debug("Skipping discovered link persistence below priority threshold",
				"task_id", task.ID,
				"priority", priority,
				"min_priority", deps.MinPriority,
			)
			return
		}

		baseURL, baseErr := url.Parse(sourceURL)

		if err := ctx.Err(); err != nil {
			jobsLog.Debug("Skipping discovered link processing: parent task context is done",
				"error", err,
				"job_id", task.JobID,
				"domain", task.DomainName,
				"task_id", task.ID,
			)
			return
		}

		var filtered []string
		var blockedCount int
		for _, link := range links {
			linkURL, err := url.Parse(link)
			if err != nil {
				continue
			}

			if !linkURL.IsAbs() {
				if baseErr != nil || baseURL == nil {
					continue
				}
				linkURL = baseURL.ResolveReference(linkURL)
			}

			if isLinkAllowedForTask(linkURL.Hostname(), task) {
				linkURL.Fragment = ""
				if linkURL.Path != "/" && strings.HasSuffix(linkURL.Path, "/") {
					linkURL.Path = strings.TrimSuffix(linkURL.Path, "/")
				}

				if robotsRules != nil && !crawler.IsPathAllowed(robotsRules, linkURL.Path) {
					blockedCount++
					jobsLog.Debug("Link blocked by robots.txt",
						"url", linkURL.String(),
						"path", linkURL.Path,
						"source", sourceURL,
					)
					continue
				}

				filtered = append(filtered, linkURL.String())
			}
		}

		if blockedCount > 0 {
			jobsLog.Debug("Filtered discovered links against robots.txt",
				"task_id", task.ID,
				"blocked_count", blockedCount,
				"allowed_count", len(filtered),
			)
		}

		if len(filtered) == 0 {
			return
		}

		linkCtxTimeout := discoveredLinksDBTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= discoveredLinksMinRemain {
				jobsLog.Warn("Skipping discovered link persistence: task deadline too close",
					"job_id", task.JobID,
					"domain", task.DomainName,
					"task_id", task.ID,
					"remaining", remaining,
				)
				return
			}

			maxTimeout := remaining - discoveredLinksMinRemain
			if maxTimeout < linkCtxTimeout {
				linkCtxTimeout = maxTimeout
			}
		}
		if linkCtxTimeout < discoveredLinksMinTimeout {
			jobsLog.Warn("Skipping discovered link persistence: insufficient timeout budget",
				"job_id", task.JobID,
				"domain", task.DomainName,
				"task_id", task.ID,
				"timeout", linkCtxTimeout,
			)
			return
		}
		// Derive from parent ctx so CancelJob propagates — otherwise a late
		// enqueue could repopulate a cancelled job with fresh pending work.
		linkCtx, linkCancel := context.WithTimeout(ctx, linkCtxTimeout)
		defer linkCancel()

		pageIDs, hosts, paths, err := db.CreatePageRecords(linkCtx, deps.DBQueue, domainID, task.DomainName, filtered)
		if err != nil {
			jobsLog.Error("Failed to create page records for links", "error", err)
			return
		}

		pagesToEnqueue := make([]db.Page, len(pageIDs))
		for i := range pageIDs {
			pagesToEnqueue[i] = db.Page{
				ID:       pageIDs[i],
				Host:     hosts[i],
				Path:     paths[i],
				Priority: priority,
			}
		}

		if err := deps.JobManager.EnqueueJobURLs(linkCtx, task.JobID, pagesToEnqueue, "link", sourceURL); err != nil {
			jobsLog.Error("Failed to enqueue discovered links", "error", err)
			return
		}
	}

	if isHomepage {
		jobsLog.Debug("Processing links from HOMEPAGE", "task_id", task.ID)
		processLinkCategory(links["header"], 1.000)
		processLinkCategory(links["footer"], 0.990)
		processLinkCategory(links["body"], task.PriorityScore*0.9)
	} else {
		jobsLog.Debug("Processing links from regular page", "task_id", task.ID)
		processLinkCategory(links["body"], task.PriorityScore*0.9)
	}
}

func normaliseComparableHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return strings.TrimPrefix(host, "www.")
}

func sameRegistrableDomain(hostA, hostB string) bool {
	normalisedA := normaliseComparableHost(hostA)
	normalisedB := normaliseComparableHost(hostB)
	if normalisedA == "" || normalisedB == "" {
		return false
	}

	rootA, errA := publicsuffix.EffectiveTLDPlusOne(normalisedA)
	rootB, errB := publicsuffix.EffectiveTLDPlusOne(normalisedB)
	if errA != nil || errB != nil {
		return normalisedA == normalisedB
	}

	return rootA == rootB
}

func sameHostWithWWWEquivalence(hostA, hostB string) bool {
	return normaliseComparableHost(hostA) == normaliseComparableHost(hostB)
}

func isLinkAllowedForTask(discoveredHost string, task *Task) bool {
	if task == nil {
		return false
	}

	if task.AllowCrossSubdomainLinks {
		return sameRegistrableDomain(discoveredHost, task.DomainName)
	}

	if task.Host != "" {
		return sameHostWithWWWEquivalence(discoveredHost, task.Host)
	}

	return sameHostWithWWWEquivalence(discoveredHost, task.DomainName)
}
