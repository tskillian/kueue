# KEP-6143: Quota Release Strategy

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Preemption of &quot;plain pod&quot; workloads](#story-1-preemption-of-plain-pod-workloads)
    - [Story 2: ClusterQueue migration to new clusters](#story-2-clusterqueue-migration-to-new-clusters)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Implementation overview](#implementation-overview)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (v0.17)](#alpha-v017)
    - [Beta (v0.18)](#beta-v018)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Modify the generic reconciler instead of the Pod controller](#modify-the-generic-reconciler-instead-of-the-pod-controller)
  - [Configuration API knob instead of feature gates](#configuration-api-knob-instead-of-feature-gates)
<!-- /toc -->

## Summary

This KEP aims to standardize the quota release strategy for terminating jobs,
and to allow administrators to choose between different strategies. Currently,
the Pod integration holds quota until pods reach a terminal phase, while most
other integrations (e.g., batch/v1 Job) release quota as soon as pods begin
terminating. This inconsistency leads to unnecessarily delayed admission and
serialized preemption for Pod workloads.

## Motivation

When Kueue preempts a Pod-based workload, the current Pod integration holds
onto its quota reservation until all Pods have fully terminated (reached
`Succeeded` or `Failed` phase). This termination often takes tens of seconds
or even minutes (and has no practical upper bound, depending on how the pod's
graceful shutdown period is configured). While the pod is terminating, ALL
pods in each cluster queue is blocked from triggering further preemptions until
it's finished because the terminating pod is always each potential preemptor pod's
ideal preemption target. This creates a large bottleneck for preemption
that does not occur for preemptions of other types of workloads.

Ultimately, this behavioral inconsistency between integrations leads to:
- Delayed admission of higher-priority workloads during preemption
- Serialized preemption, where each ClusterQueue is head-of-line blocking causing
  the scheduler to wait for one preemption within the ClusterQueue to fully complete
  before starting the next

### Goals

- Standardize quota release behavior across integrations by introducing a
  `FastQuotaRelease` feature gate (Alpha, disabled by default) that releases
  quota as soon as all Pods have a `deletionTimestamp`, aligning with current
  Job integration behavior.

### Non-Goals

- Changing quota release behavior for non-Pod integrations (they already
  release quota when their upstream controller reports no active pods).

## Proposal

Introduce two strategies for releasing quota when Jobs are terminating (either
due to eviction or finish):

- **Fast quota release**: Release quota as soon as all Pods have a
  `deletionTimestamp`, regardless of whether they are still running. This is
  how the batch/v1 Job integration already behaves, since the Kubernetes Job
  controller does not count terminating pods in `status.active`.
- **Delayed quota release**: Release quota only after all Pods have reached a
  terminal phase (`Succeeded` or `Failed`). This is the current Pod integration
  behavior and preserves strict quota tracking for environments (e.g.,
  fixed-size clusters without autoscaling) where temporary quota
  oversubscription is not acceptable. This mode is "opportunistic" — only for
  integrations which support it by surfacing the required information in their
  status.

### User Stories

#### Story 1: Preemption of "plain pod" workloads

As a cluster administrator, I run long-running Pod workloads managed by Kueue
with a termination grace period of 60 seconds. When a higher-priority workload
arrives and Kueue preempts my running workload, I want the higher-priority
workloads to start running as quickly as possible while still honoring
graceful shutdown periods of preempted pods.

#### Story 2: ClusterQueue migration to new clusters

As a cluster administrator, I migrate ClusterQueues to a new cluster one by one.
Because burst is free for the initial CQs, these CQs will burst past their
nominal quota. I want subsequent CQs to be able to reclaim their nominal quota
within a reasonable amount of time.

### Notes/Constraints/Caveats

### Risks and Mitigations

**Risk**: Fast quota release could cause an increase in node churn if the
cluster autoscaler is being used. While pods are pending, the cluster
autoscaler may trigger scale-up even though pods could be terminating on
existing nodes.

**Mitigation**: This is the same behavior that already exists for the Job
integration and other integrations, so it's a well established/understood
behavior.

**Risk**: With fast quota release, if a pod gets "stuck" in a terminating
state, there may be a discrepancy between the amount of quota available and the
actual resources available on the cluster. In fixed-size clusters where the sum
of nominal quotas strictly equals cluster capacity, this can cause temporary
capacity oversubscription.

**Mitigation**: The feature is behind a feature gate (disabled by default),
allowing administrators to opt in only when appropriate for their environment.
Additionally, [setup failure recovery](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_failure_recovery/)
provides a failure recovery mechanism that automatically transitions
pods into the Failed phase when they are assigned to unreachable nodes and stuck
terminating.

## Design Details

The `FastQuotaRelease` feature gate will be introduced as Alpha (disabled by
default) in v0.17, with backports to v0.15 and v0.16 (also disabled by
default).

### Implementation overview

Both strategies delegate to the individual integrations via the `job.IsActive()`
function.

For the **fast quota release** strategy, integrations answer `IsActive()=false`
as soon as all Pods have a `deletionTimestamp`. The initial implementation is
scoped to the Pod integration in `pkg/controller/jobs/pod/pod_controller.go`,
since other integrations (Job, JobSet, KubeflowJob, TrainJob) already
effectively implement this behavior by relying on their upstream controller's
`status.active` field, which does not count terminating pods.

For the **delayed quota release** strategy, integrations answer
`IsActive()=false` once all Pods are terminal. However, because some Jobs may
not provide this information in their status, the implementation is
"opportunistic" and can be extended gradually as integrations implement such
functionality.

When the `FastQuotaRelease` feature gate is enabled, the Pod integration's
`IsActive()` method is modified to treat any Pod with a `deletionTimestamp` as
inactive. When disabled, the existing behavior is preserved: a Pod is only
considered inactive once it has reached a terminal phase (`Succeeded` or
`Failed`).

No changes are needed to the generic reconciler. The existing flow already
handles this:

1. When a workload is evicted (e.g., by preemption), the reconciler calls
   `job.IsActive()` to check if the job is still active.
2. If `IsActive()` returns false, the reconciler clears the workload's
   admission, releasing quota.
3. The released quota becomes available for the next scheduling cycle.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

- `pkg/controller/jobs/pod`: Test `IsActive()` with the feature gate enabled
  and disabled, covering:
  - Pod with `deletionTimestamp` and feature gate enabled returns inactive.
  - Pod with `deletionTimestamp` and feature gate disabled returns active
    (unless in terminal phase).
  - Mixed groups with some terminating and some running Pods.
  - Single Pod (non-group) behavior.

#### Integration tests

- Test that when a Pod workload is evicted with the feature gate enabled, quota
  is released as soon as all Pods have a `deletionTimestamp`.
- Test that preempted workloads are readmitted promptly after the preempted
  Pods begin terminating.

#### e2e tests

None required for Alpha.

### Graduation Criteria

#### Alpha (v0.17)

- `FastQuotaRelease` feature gate (disabled by default)
- Backport to v0.15 and v0.16 with feature gate disabled by default

#### Beta (v0.18)

- Re-evaluate introducing a Configuration API knob (e.g.,
  `.scheduling.quotaReleaseStrategy`) to switch between the strategies
- Re-evaluate the enablement of `FastQuotaRelease` by default
- Address feedback from Alpha users

## Implementation History

## Alternatives

### Modify the generic reconciler instead of the Pod controller

Instead of changing just the Pod controller's `IsActive()` logic, the reconciler
could be modified to release quota for any workload as soon as it's marked for
preemption. This was considered because it would provide a single,
integration-agnostic mechanism. However, the `job.IsActive()` approach was
preferred because:
- It keeps the change scoped and non-invasive
- The job-specific `IsActive()` implementation is the established pattern for
  controlling when to release quota
- It maintains consistency with how other integrations already work

### Configuration API knob instead of feature gates

Instead of feature gates, a Configuration API field (e.g.,
`.scheduling.quotaReleaseStrategy: OnTermination | OnTerminal`) could be
introduced to allow administrators to select the quota release strategy. This
approach may be adopted in Beta (v0.18) to replace the feature gate, allowing
more granular control and avoiding the need for feature gate promotion.
