/*
Copyright (c) Amazon Web Services
Distributed under the terms of the MIT license
*/

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	workspacev1alpha1 "github.com/jupyter-infra/jupyter-k8s/api/v1alpha1"
	"github.com/jupyter-infra/jupyter-k8s/internal/controller"
)

func boolPtr(value bool) *bool {
	return &value
}

var _ = Describe("WorkspaceTemplate Webhook", func() {
	Context("validateTemplateDefaultVolumes", func() {
		It("should allow valid default volumes", func() {
			sizeLimit := resource.MustParse("1Gi")
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{
							Name:      "shm",
							MountPath: "/dev/shm",
							EmptyDir: &corev1.EmptyDirVolumeSource{
								Medium:    corev1.StorageMediumMemory,
								SizeLimit: &sizeLimit,
							},
						},
					},
				},
			}

			Expect(validateTemplateDefaultVolumes(template)).To(BeNil())
		})

		It("should reject default volumes without a source", func() {
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{Name: "broken", MountPath: "/broken"},
					},
				},
			}

			violation := validateTemplateDefaultVolumes(template)
			Expect(violation).NotTo(BeNil())
			Expect(violation.Type).To(Equal(ViolationTypeInvalidVolumeSource))
			Expect(violation.Field).To(Equal("spec.defaultVolumes[0]"))
		})

		It("should reject duplicate default volume names", func() {
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{Name: "data", MountPath: "/data", EmptyDir: &corev1.EmptyDirVolumeSource{}},
						{Name: "data", MountPath: "/other", EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				},
			}

			violation := validateTemplateDefaultVolumes(template)
			Expect(violation).NotTo(BeNil())
			Expect(violation.Type).To(Equal(ViolationTypeInvalidVolumeConfiguration))
			Expect(violation.Field).To(Equal("spec.defaultVolumes[1].name"))
		})

		It("should reject duplicate default volume mount paths", func() {
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{Name: "data", MountPath: "/data", EmptyDir: &corev1.EmptyDirVolumeSource{}},
						{Name: "cache", MountPath: "/data", EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				},
			}

			violation := validateTemplateDefaultVolumes(template)
			Expect(violation).NotTo(BeNil())
			Expect(violation.Type).To(Equal(ViolationTypeInvalidVolumeConfiguration))
			Expect(violation.Field).To(Equal("spec.defaultVolumes[1].mountPath"))
		})

		It("should reject default volumes that conflict with explicit primary storage default mount path", func() {
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					PrimaryStorage: &workspacev1alpha1.StorageConfig{
						DefaultMountPath: "/workspace",
					},
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{Name: "cache", MountPath: "/workspace", EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				},
			}

			violation := validateTemplateDefaultVolumes(template)
			Expect(violation).NotTo(BeNil())
			Expect(violation.Type).To(Equal(ViolationTypeInvalidVolumeConfiguration))
		})

		It("should reject default volumes that conflict with implicit primary storage mount path", func() {
			template := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					PrimaryStorage: &workspacev1alpha1.StorageConfig{
						DefaultSize: resource.MustParse("1Gi"),
					},
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{Name: "home-shadow", MountPath: controller.DefaultMountPath, EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				},
			}

			violation := validateTemplateDefaultVolumes(template)
			Expect(violation).NotTo(BeNil())
			Expect(violation.Type).To(Equal(ViolationTypeInvalidVolumeConfiguration))
		})
	})

	Context("constraintsChanged", func() {
		It("should detect changes to default volumes", func() {
			oldTemplate := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{},
			}
			newTemplate := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					DefaultVolumes: []workspacev1alpha1.VolumeSpec{
						{
							Name:      "shm",
							MountPath: "/dev/shm",
							EmptyDir: &corev1.EmptyDirVolumeSource{
								Medium: corev1.StorageMediumMemory,
							},
						},
					},
				},
			}

			Expect(constraintsChanged(oldTemplate, newTemplate)).To(BeTrue())
		})

		It("should detect changes to allowSecondaryStorages", func() {
			oldTemplate := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					AllowSecondaryStorages: boolPtr(true),
				},
			}
			newTemplate := &workspacev1alpha1.WorkspaceTemplate{
				Spec: workspacev1alpha1.WorkspaceTemplateSpec{
					AllowSecondaryStorages: boolPtr(false),
				},
			}

			Expect(constraintsChanged(oldTemplate, newTemplate)).To(BeTrue())
		})
	})
})
