/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package v1alpha1

import (
	"context"
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workspacev1alpha1 "github.com/jupyter-infra/jupyter-k8s/api/v1alpha1"
	"github.com/jupyter-infra/jupyter-k8s/internal/controller"
)

const reservedWorkspaceStorageVolumeName = "workspace-storage"

// VolumeValidator handles volume validation for webhooks
type VolumeValidator struct {
	client client.Client
}

// NewVolumeValidator creates a new VolumeValidator
func NewVolumeValidator(k8sClient client.Client) *VolumeValidator {
	return &VolumeValidator{
		client: k8sClient,
	}
}

// ValidateWorkspaceVolumes checks structural volume validity that applies to all users.
func (vv *VolumeValidator) ValidateWorkspaceVolumes(workspace *workspacev1alpha1.Workspace) error {
	if violation := validateVolumeSources(workspace.Spec.Volumes); violation != nil {
		return fmt.Errorf("workspace has invalid volume sources: %s", violation.Message)
	}
	if violation := validateVolumeMountConflicts(workspace); violation != nil {
		return fmt.Errorf("workspace has invalid volume configuration: %s", violation.Message)
	}
	return nil
}

// ValidateVolumeOwnership checks that volumes don't reference PVCs owned by other workspaces
func (vv *VolumeValidator) ValidateVolumeOwnership(ctx context.Context, workspace *workspacev1alpha1.Workspace) error {
	if violation := validateVolumeOwnership(ctx, vv.client, workspace); violation != nil {
		return fmt.Errorf("workspace violates volume ownership constraints: %s", violation.Message)
	}
	return nil
}

// validateSecondaryStorages checks if secondary storage volumes are allowed by template
func validateSecondaryStorages(volumes []workspacev1alpha1.VolumeSpec, template *workspacev1alpha1.WorkspaceTemplate) *TemplateViolation {
	for _, defaultVolume := range template.Spec.DefaultVolumes {
		matchingVolume := findVolumeByName(volumes, defaultVolume.Name)
		if matchingVolume == nil {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   "spec.volumes",
				Message: fmt.Sprintf("Template '%s' requires default volume '%s', but it is missing from the workspace", template.Name, defaultVolume.Name),
				Allowed: "template default volumes must be preserved",
				Actual:  "missing default volume",
			}
		}
		if !volumeSpecEqual(*matchingVolume, defaultVolume) {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("spec.volumes[%s]", defaultVolume.Name),
				Message: fmt.Sprintf("Template '%s' requires default volume '%s' to match the template definition", template.Name, defaultVolume.Name),
				Allowed: "template default volume definition",
				Actual:  "workspace volume differs from template",
			}
		}
	}

	// Skip further validation if no volumes specified and the template doesn't require any.
	if len(volumes) == 0 {
		return nil
	}

	// Check AllowSecondaryStorages setting (default is true if not specified)
	if template.Spec.AllowSecondaryStorages != nil && !*template.Spec.AllowSecondaryStorages {
		for _, volume := range volumes {
			if !containsEquivalentVolume(template.Spec.DefaultVolumes, volume) {
				return &TemplateViolation{
					Type:    ViolationTypeSecondaryStorageNotAllowed,
					Field:   "spec.volumes",
					Message: fmt.Sprintf("Template '%s' does not allow additional storage volumes, but workspace specifies volume '%s'", template.Name, volume.Name),
					Allowed: "template default volumes only",
					Actual:  fmt.Sprintf("additional volume '%s'", volume.Name),
				}
			}
		}
	}

	return nil
}

func validateTemplateDefaultVolumes(template *workspacev1alpha1.WorkspaceTemplate) *TemplateViolation {
	if violation := validateVolumeSourcesAtPath(template.Spec.DefaultVolumes, "spec.defaultVolumes"); violation != nil {
		return violation
	}

	reservedMountPaths := map[string]string{}
	if template.Spec.PrimaryStorage != nil {
		if template.Spec.PrimaryStorage.DefaultMountPath != "" {
			reservedMountPaths[template.Spec.PrimaryStorage.DefaultMountPath] = "template primary storage"
		} else if !template.Spec.PrimaryStorage.DefaultSize.IsZero() {
			reservedMountPaths[controller.DefaultMountPath] = "template primary storage"
		}
	}

	return validateVolumeMountConflictsAtPath(template.Spec.DefaultVolumes, "spec.defaultVolumes", reservedMountPaths)
}

func containsEquivalentVolume(volumes []workspacev1alpha1.VolumeSpec, expected workspacev1alpha1.VolumeSpec) bool {
	for _, volume := range volumes {
		if volumeSpecEqual(volume, expected) {
			return true
		}
	}

	return false
}

func findVolumeByName(volumes []workspacev1alpha1.VolumeSpec, name string) *workspacev1alpha1.VolumeSpec {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}

	return nil
}

func volumeSpecEqual(left, right workspacev1alpha1.VolumeSpec) bool {
	return equality.Semantic.DeepEqual(left, right)
}

func validateVolumeSources(volumes []workspacev1alpha1.VolumeSpec) *TemplateViolation {
	return validateVolumeSourcesAtPath(volumes, "spec.volumes")
}

func validateVolumeSourcesAtPath(volumes []workspacev1alpha1.VolumeSpec, fieldPrefix string) *TemplateViolation {
	for i, volume := range volumes {
		field := fmt.Sprintf("%s[%d]", fieldPrefix, i)
		if volume.Name == "" {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.name", field),
				Message: "volume name must not be empty",
				Allowed: "non-empty volume name",
				Actual:  "empty volume name",
			}
		}
		if errs := validation.IsDNS1123Label(volume.Name); len(errs) > 0 {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.name", field),
				Message: fmt.Sprintf("volume name '%s' must be a valid Kubernetes volume name: %s", volume.Name, strings.Join(errs, "; ")),
				Allowed: "DNS-1123 label",
				Actual:  volume.Name,
			}
		}
		if volume.MountPath == "" {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.mountPath", field),
				Message: fmt.Sprintf("volume '%s' mountPath must not be empty", volume.Name),
				Allowed: "non-empty mount path",
				Actual:  "empty mount path",
			}
		}
		if !path.IsAbs(volume.MountPath) {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.mountPath", field),
				Message: fmt.Sprintf("volume '%s' mountPath must be an absolute path", volume.Name),
				Allowed: "absolute mount path",
				Actual:  volume.MountPath,
			}
		}

		hasPVC := volume.PersistentVolumeClaimName != nil
		hasEmptyDir := volume.EmptyDir != nil

		if hasPVC == hasEmptyDir {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeSource,
				Field:   field,
				Message: fmt.Sprintf("volume '%s' must specify exactly one source: persistentVolumeClaimName or emptyDir", volume.Name),
				Allowed: "exactly one source",
				Actual:  "zero or multiple sources",
			}
		}
		if hasPVC {
			if *volume.PersistentVolumeClaimName == "" {
				return &TemplateViolation{
					Type:    ViolationTypeInvalidVolumeSource,
					Field:   fmt.Sprintf("%s.persistentVolumeClaimName", field),
					Message: fmt.Sprintf("volume '%s' persistentVolumeClaimName must not be empty", volume.Name),
					Allowed: "non-empty PVC name",
					Actual:  "empty PVC name",
				}
			}
			if errs := validation.IsDNS1123Subdomain(*volume.PersistentVolumeClaimName); len(errs) > 0 {
				return &TemplateViolation{
					Type:    ViolationTypeInvalidVolumeConfiguration,
					Field:   fmt.Sprintf("%s.persistentVolumeClaimName", field),
					Message: fmt.Sprintf("volume '%s' persistentVolumeClaimName '%s' must be a valid Kubernetes object name: %s", volume.Name, *volume.PersistentVolumeClaimName, strings.Join(errs, "; ")),
					Allowed: "DNS-1123 subdomain",
					Actual:  *volume.PersistentVolumeClaimName,
				}
			}
		}
		if hasEmptyDir && volume.EmptyDir.Medium != "" && volume.EmptyDir.Medium != corev1.StorageMediumMemory {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.emptyDir.medium", field),
				Message: fmt.Sprintf("volume '%s' emptyDir.medium must be empty or Memory", volume.Name),
				Allowed: "empty string or Memory",
				Actual:  string(volume.EmptyDir.Medium),
			}
		}
	}

	return nil
}

func validateVolumeMountConflicts(workspace *workspacev1alpha1.Workspace) *TemplateViolation {
	seenMountPaths := map[string]string{}

	if workspace.Spec.Storage != nil {
		storageMountPath := workspace.Spec.Storage.MountPath
		if storageMountPath == "" {
			storageMountPath = controller.DefaultMountPath
		}
		seenMountPaths[storageMountPath] = "workspace primary storage"
	}

	return validateVolumeMountConflictsAtPath(workspace.Spec.Volumes, "spec.volumes", seenMountPaths)
}

func validateVolumeMountConflictsAtPath(volumes []workspacev1alpha1.VolumeSpec, fieldPrefix string, seenMountPaths map[string]string) *TemplateViolation {
	seenNames := map[string]string{
		reservedWorkspaceStorageVolumeName: "workspace primary storage",
	}

	for i, volume := range volumes {
		field := fmt.Sprintf("%s[%d]", fieldPrefix, i)
		if owner, exists := seenNames[volume.Name]; exists {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.name", field),
				Message: fmt.Sprintf("volume name '%s' conflicts with %s", volume.Name, owner),
				Allowed: "unique volume names",
				Actual:  volume.Name,
			}
		}
		seenNames[volume.Name] = field

		if owner, exists := seenMountPaths[volume.MountPath]; exists {
			return &TemplateViolation{
				Type:    ViolationTypeInvalidVolumeConfiguration,
				Field:   fmt.Sprintf("%s.mountPath", field),
				Message: fmt.Sprintf("mount path '%s' conflicts with %s", volume.MountPath, owner),
				Allowed: "unique mount paths",
				Actual:  volume.MountPath,
			}
		}
		seenMountPaths[volume.MountPath] = field
	}

	return nil
}

// validateVolumeOwnership checks that volumes don't reference PVCs owned by other workspaces
func validateVolumeOwnership(ctx context.Context, k8sClient client.Client, workspace *workspacev1alpha1.Workspace) *TemplateViolation {
	for _, volume := range workspace.Spec.Volumes {
		if volume.PersistentVolumeClaimName == nil || *volume.PersistentVolumeClaimName == "" {
			continue
		}

		// Get the PVC
		pvc := &corev1.PersistentVolumeClaim{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      *volume.PersistentVolumeClaimName,
			Namespace: workspace.Namespace,
		}, pvc)

		// If PVC doesn't exist, skip validation (let other validation handle it)
		if err != nil {
			continue
		}

		// Check if PVC is owned by another workspace
		for _, ownerRef := range pvc.OwnerReferences {
			if ownerRef.APIVersion == "workspace.jupyter.org/v1alpha1" &&
				ownerRef.Kind == "Workspace" &&
				ownerRef.UID != workspace.UID {
				return &TemplateViolation{
					Type:    ViolationTypeVolumeOwnedByAnotherWorkspace,
					Field:   fmt.Sprintf("spec.volumes[%s].persistentVolumeClaimName", volume.Name),
					Message: fmt.Sprintf("Volume '%s' references PVC '%s' which is owned by another workspace '%s'", volume.Name, *volume.PersistentVolumeClaimName, ownerRef.Name),
					Allowed: "PVCs not owned by other workspaces",
					Actual:  fmt.Sprintf("PVC owned by workspace '%s'", ownerRef.Name),
				}
			}
		}
	}

	return nil
}
