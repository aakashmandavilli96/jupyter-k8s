# One-pager: Add `emptyDir`-backed pod volumes to Workspace CRDs (2026-08-20)

## Executive Summary

This change adds pod-scoped `emptyDir` volumes to the `Workspace` API so a workspace mounts `/dev/shm` from RAM while keeping the existing persistent home volume on a PVC. The design extends `spec.volumes[]`, which previously accepted only existing PVCs, so each entry uses either `persistentVolumeClaimName` or `emptyDir`. It also adds template-managed `defaultVolumes` so an admin injects a memory-backed `/dev/shm` mount for every workspace created from a `WorkspaceTemplate`.

This design keeps the existing `spec.storage` contract intact. `spec.storage` remains the single operator-managed persistent home volume. `spec.volumes[]` becomes the place for additional pod volumes with pod lifetime semantics or shared PVC reuse. This maps directly to the current controller flow in `internal/controller/deployment_builder.go` and keeps the emptyDir use case in the same part of the API where users already ask for secondary mounts.

Decision record:
1. Use the union model for `spec.volumes[]` instead of adding a dedicated `shmSize` field.
2. Use `WorkspaceTemplate.spec.defaultVolumes` as the mechanism for template-wide SHM defaults.

## Problem

Before this change, a workspace pod mounted one operator-managed PVC through `spec.storage` and zero or more user-supplied PVCs through `spec.volumes[]`. The API in `api/v1alpha1/workspace_types.go` encoded `spec.volumes[]` as `name + persistentVolumeClaimName + mountPath`, and the pod builder in `internal/controller/deployment_builder.go` rendered those entries as `PersistentVolumeClaimVolumeSource`. The API did not express the Kubernetes volume shape required for shared memory:

```yaml
emptyDir:
  medium: Memory
  sizeLimit: 1Gi
```

That gap blocks `/dev/shm` for notebook and ML workloads that use Python multiprocessing, PyTorch dataloaders, Chromium, or similar shared-memory consumers. Those workloads need pod lifetime memory storage. They do not need or want a second PVC for this path. The new API must support that pod-scoped mount while preserving the existing persistent workspace volume.

The operator does not hard-code a single `/dev/shm` size. Shared-memory pressure depends on the customer's workload, container memory limits, node shape, and concurrency model. The API exposes the Kubernetes `emptyDir` knobs, templates provide recommended defaults for workload profiles, and cluster policy enforces organization-specific caps when needed.

## Proposal

The design keeps `spec.storage` unchanged and generalizes `spec.volumes[]` into additional pod volumes with a volume-source union. Because the project is not live yet, this takes the clean API now instead of adding SHM as a one-off special case.

```go
type VolumeSpec struct {
    Name      string `json:"name"`
    MountPath string `json:"mountPath"`

    // Exactly one source must be set.
    PersistentVolumeClaimName *string                       `json:"persistentVolumeClaimName,omitempty"`
    EmptyDir                  *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`
}
```

Example workspace with persistent home storage and memory-backed SHM:

```yaml
apiVersion: workspace.jupyter.org/v1alpha1
kind: Workspace
metadata:
  name: notebook-with-shm
spec:
  displayName: Notebook with SHM
  image: jupyter/scipy-notebook:latest
  desiredStatus: Running
  storage:
    size: 20Gi
  volumes:
    - name: shm
      mountPath: /dev/shm
      emptyDir:
        medium: Memory
        sizeLimit: 1Gi
    - name: shared-data
      mountPath: /data
      persistentVolumeClaimName: shared-data-pvc
```

`WorkspaceTemplate` defines admin-managed default volumes and continues using `allowSecondaryStorages` to control whether end users are allowed to add extra mounts beyond those defaults:

```go
type WorkspaceTemplateSpec struct {
    ...
    DefaultVolumes         []VolumeSpec `json:"defaultVolumes,omitempty"`
    AllowSecondaryStorages *bool        `json:"allowSecondaryStorages,omitempty"`
}
```

Example template default:

```yaml
spec:
  defaultVolumes:
    - name: shm
      mountPath: /dev/shm
      emptyDir:
        medium: Memory
        sizeLimit: 1Gi
  allowSecondaryStorages: false
```

This gives platform admins a clean way to standardize SHM while preventing arbitrary user-specified extra mounts.

## Behavior and Validation

The webhook and controller changes fit the existing defaulting, validation, and pod-build stages.

On defaulting, `WorkspaceTemplate.spec.defaultVolumes` are appended to the workspace during admission when that volume name is not already present. Admission validation rejects duplicate volume names and duplicate mount paths across template-provided volumes, `spec.storage.mountPath`, and workspace-provided volumes. `WorkspaceTemplate.spec.defaultVolumes` are validated when the template is created or updated, so invalid template defaults fail before a user creates a workspace from that template. That keeps the rendered pod spec deterministic and prevents a user from shadowing the persistent home mount or a template-managed `/dev/shm` mount.

On validation, `spec.volumes[]` requires exactly one source. The reserved name `workspace-storage` remains blocked. Volume names must be Kubernetes DNS-1123 labels, PVC names must be DNS-1123 subdomains, and mount paths must be absolute. Template default volumes must remain present and must match the template definition. `allowSecondaryStorages=false` rejects any workspace volume that is not one of the template defaults. PVC ownership checks from `internal/webhook/v1alpha1/volume_validator.go` apply only to PVC-backed entries. `emptyDir` entries need no ownership lookup because they are pod-scoped and disappear with the pod.

On reconciliation, `internal/controller/deployment_builder.go` switches on the selected source and emits either `PersistentVolumeClaimVolumeSource` or `EmptyDirVolumeSource`. The operator-managed PVC flow in `spec.storage` and `internal/controller/pvc_builder.go` does not change.

## ValidatingAdmissionPolicy Fit

Kubernetes `ValidatingAdmissionPolicy` is useful for cluster-level CEL policies, but it is not the primary validation mechanism for this CRD change. The API already carries portable structural validation through CRD schema and CEL markers: `VolumeSpec` requires exactly one of `persistentVolumeClaimName` or `emptyDir`, non-empty Kubernetes-compatible names, absolute mount paths, and rejection of the reserved `workspace-storage` volume name. Those rules travel with the CRD and fail before the controller renders a pod.

The validating webhook still owns the dynamic rules that CEL policy is not a clean fit for: preserving `WorkspaceTemplate.spec.defaultVolumes`, enforcing `allowSecondaryStorages`, checking duplicate mount paths against defaulted primary storage, and validating PVC ownership. `emptyDir` entries skip the PVC ownership lookup because there is no referenced storage object to authorize.

`ValidatingAdmissionPolicy` remains optional defense-in-depth for platform administrators who want a cluster-wide guardrail independent of this operator. Example policies reject memory-backed `emptyDir` volumes without `sizeLimit`, cap `emptyDir.sizeLimit`, or only allow `emptyDir.medium: Memory` on `/dev/shm`. If applied to rendered Pods, a rejected Pod leaves the workspace unable to start; if applied to `Workspace` objects, the policy duplicates CRD/webhook rules. For this implementation, document optional policy examples separately instead of requiring them for correctness.

## Alternatives Considered

A dedicated `spec.sharedMemory` or `spec.shmSize` field solves only the narrow `/dev/shm` case. It hard-codes one mount path and creates a second configuration model for volumes. That design forces another CRD change when the next pod-scoped mount is requested. Extending `spec.volumes[]` solves SHM now and keeps one coherent API for extra mounts.

Allowing raw pod-spec patches was also considered and rejected. Storage is a core part of the `Workspace` contract. It belongs in the CRD with validation, template policy, and sample manifests.

## Implementation Scope

The code change touches four places:
1. `api/v1alpha1/workspace_types.go` and `api/v1alpha1/workspacetemplate_types.go` for the CRD shape.
2. `internal/webhook/v1alpha1` for defaulting and validation.
3. `internal/controller/deployment_builder.go` for pod volume rendering.
4. `config/samples` and unit tests for PVC, SHM, and template-default coverage.

We do not need to change the primary persistent storage model, AccessStrategy resources, or the workspace status contract for this work.

## Review Status and Next Steps

The branch includes the API, controller, webhook, CRD, Helm chart, sample, and unit-test changes for this design. Before merge, run the project validation suite and publish optional `ValidatingAdmissionPolicy` examples only when platform administrators request cluster-level SHM caps.
