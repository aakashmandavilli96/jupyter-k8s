/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package v1alpha1

import workspacev1alpha1 "github.com/jupyter-infra/jupyter-k8s/api/v1alpha1"

// applyVolumeDefaults appends template-defined volumes that are missing from the workspace.
// Matching is by volume name so the operation remains idempotent across repeated webhook calls.
func applyVolumeDefaults(workspace *workspacev1alpha1.Workspace, template *workspacev1alpha1.WorkspaceTemplate) {
	if len(template.Spec.DefaultVolumes) == 0 {
		return
	}

	existingVolumes := make(map[string]struct{}, len(workspace.Spec.Volumes))
	for _, volume := range workspace.Spec.Volumes {
		existingVolumes[volume.Name] = struct{}{}
	}

	for _, defaultVolume := range template.Spec.DefaultVolumes {
		if _, exists := existingVolumes[defaultVolume.Name]; exists {
			continue
		}

		workspace.Spec.Volumes = append(workspace.Spec.Volumes, *defaultVolume.DeepCopy())
		existingVolumes[defaultVolume.Name] = struct{}{}
	}
}
