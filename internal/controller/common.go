package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	scmv1alpha1 "github.com/jalet/scm-metrics-exporter/api/v1alpha1"
	"github.com/jalet/scm-metrics-exporter/internal/discovery"
)

const (
	conditionReady          = "Ready"
	reasonDiscovered        = "Discovered"
	reasonCredentialInvalid = "CredentialsInvalid"
	reasonDiscoveryFailed   = "DiscoveryFailed"
	reasonDispatchFailed    = "DispatchFailed"
	reasonRateLimited       = "RateLimited"

	// credentialRequeue backs off after a credentials/discovery failure; pendingRequeue
	// tops up the collection-Job pool as running Jobs finish (parallelism cap reached).
	credentialRequeue = time.Minute
	pendingRequeue    = 30 * time.Second

	// rateLimitBuffer pads the requeue past the provider's reported reset so the next pass
	// sees the window already refilled rather than racing the boundary.
	rateLimitBuffer = 15 * time.Second
)

// rateLimitRequeue returns how long to wait before retrying after a rate-limit pause: until
// the provider's reset plus a small buffer, floored at pendingRequeue so a stale, zero, or
// past reset time cannot busy-loop the reconciler.
func rateLimitRequeue(reset time.Time) time.Duration {
	d := time.Until(reset) + rateLimitBuffer
	if d < pendingRequeue {
		d = pendingRequeue
	}
	return d
}

// rateLimited reports whether the credential's remaining API budget is below threshold and,
// if so, the requeue delay until the provider's rate-limit window resets and a message for
// the Ready condition. A zero threshold or a probe error is fail-open (limited=false): a
// flaky probe must never halt collection. The probe closure adapts a provider's typed
// RateBudget func to the shared budget signal.
func rateLimited(ctx context.Context, threshold int32, probe func(context.Context) (discovery.Budget, error)) (limited bool, requeue time.Duration, msg string) {
	if threshold <= 0 {
		return false, 0, ""
	}
	b, err := probe(ctx)
	if err != nil {
		logf.FromContext(ctx).Info("rate-limit probe failed; proceeding with dispatch", "error", err.Error())
		return false, 0, ""
	}
	if !b.Known || b.Remaining >= int(threshold) {
		return false, 0, ""
	}
	return true, rateLimitRequeue(b.Reset), fmt.Sprintf(
		"API budget low (remaining=%d < %d); pausing dispatch until %s",
		b.Remaining, threshold, b.Reset.Format(time.RFC3339))
}

// setReadyCondition sets the shared Ready condition on a CR's status conditions.
func setReadyCondition(conds *[]metav1.Condition, generation int64, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// needsDiscovery reports whether the operator should re-list repositories: nothing has been
// discovered yet, or the discovery interval has elapsed since the last successful run.
func needsDiscovery(last *metav1.Time, count int, interval time.Duration) bool {
	if last == nil || count == 0 {
		return true
	}
	return time.Since(last.Time) >= interval
}

// selectorFrom maps the CR's autoDiscover block to a discovery.Selector (include + exclude).
func selectorFrom(a scmv1alpha1.AutoDiscover) discovery.Selector {
	return discovery.Selector{
		Include: filterFrom(a.Include),
		Exclude: filterFrom(a.Exclude),
	}
}

func filterFrom(f scmv1alpha1.RepoFilter) discovery.Filter {
	return discovery.Filter{
		Topics:       f.Topics,
		Visibility:   f.Visibility,
		NamePatterns: f.NamePatterns,
		Archived:     f.Archived,
	}
}

// loadSecret fetches a Secret by name from the given namespace.
func loadSecret(ctx context.Context, c client.Client, namespace, name string) (*corev1.Secret, error) {
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// dispatchJobs ensures a collection Job exists for each repo, capped so that at most
// parallelism Jobs are active (running or pending) at once. It returns pending=true when
// repos still lack a Job because the cap was hit, so the caller requeues soon to top up as
// running Jobs finish. A Job's name is deterministic per (CR, repo), so an existing Job
// (active or finished-awaiting-TTL) is never recreated within a discovery cycle.
func dispatchJobs(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	crName, namespace string,
	parallelism int32,
	repos []string,
	jobFor func(repo string) *batchv1.Job,
) (pending bool, err error) {
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs, client.InNamespace(namespace), client.MatchingLabels(selectorLabels(crName))); err != nil {
		return false, err
	}
	existing := make(map[string]bool, len(jobs.Items))
	active := 0
	for i := range jobs.Items {
		j := &jobs.Items[i]
		existing[j.Name] = true
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			active++
		}
	}

	for _, repo := range repos {
		job := jobFor(repo)
		if existing[job.Name] {
			continue
		}
		if active >= int(parallelism) {
			return true, nil // cap reached; more to dispatch on the next pass
		}
		if err := controllerutil.SetControllerReference(owner, job, scheme); err != nil {
			return false, err
		}
		if err := c.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return false, err
		}
		active++
	}
	return false, nil
}
