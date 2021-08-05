package kubernetes

import (
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	transform "github.com/konveyor/crane-lib/transform"
	"github.com/konveyor/crane-lib/transform/types"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	containerImageUpdate        = "/spec/template/spec/containers/%v/image"
	initContainerImageUpdate    = "/spec/template/spec/initContainers/%v/image"
	podContainerImageUpdate     = "/spec/containers/%v/image"
	podInitContainerImageUpdate = "/spec/initContainers/%v/image"
	annotationInitial           = `%v
{"op": "add", "path": "/metadata/annotations/%v", "value": "%v"}`
	annotationNext = `%v,
{"op": "add", "path": "/metadata/annotations/%v", "value": "%v"}`
	removeAnnotationInitial = `%v
{"op": "remove", "path": "/metadata/annotations/%v"}`
	removeAnnotationNext = `%v,
{"op": "remove", "path": "/metadata/annotations/%v"}`
	updateImageString = `[
{"op": "replace", "path": "%v", "value": "%v"}
]`
	podNodeName = `[
{"op": "remove", "path": "/spec/nodeName"}
]`

	podNodeSelector = `[
{"op": "remove", "path": "/spec/nodeSelector"}
]`

	podPriority = `[
{"op": "remove", "path": "/spec/priority"}
]`

	updateNamespaceString = `[
{"op": "replace", "path": "/metadata/namespace", "value": "%v"}
]`

	updateRoleBindingSVCACCTNamspacestring = `%v
{"op": "replace", "path": "/subjects/%v/namespace", "value": "%v"}`

	updateClusterIP = `[
{"op": "remove", "path": "/spec/clusterIP"}
]`

	updateExternalIPs = `[
{"op": "remove", "path": "/spec/externalIPs"}
]`
)

var endpointGK = schema.GroupKind{
	Group: "",
	Kind:  "Endpoints",
}

var endpointSliceGK = schema.GroupKind{
	Group: "discovery.k8s.io",
	Kind:  "EndpointSlice",
}

var pvcGK = schema.GroupKind{
	Group: "",
	Kind:  "PersistentVolumeClaim",
}

var podGK = schema.GroupKind{
	Group: "",
	Kind:  "Pod",
}

var serviceGK = schema.GroupKind{
	Group: "",
	Kind:  "Service",
}

type KubernetesTransformPlugin struct {
	AddAnnotations      map[string]string
	RemoveAnnotations   []string
	RegistryReplacement map[string]string
	NewNamespace        string
}

func (k KubernetesTransformPlugin) setOptionalFields(extras map[string]string) {
	k.NewNamespace = extras["NewNamespace"]
	if len(extras["AddAnnotations"]) > 0 {
		k.AddAnnotations = transform.ParseOptionalFieldMapVal(extras["AddAnnotations"])
	}
	if len(extras["RemoveAnnotations"]) > 0 {
		k.RemoveAnnotations = transform.ParseOptionalFieldSliceVal(extras["RemoveAnnotations"])
	}
	if len(extras["RegistryReplacement"]) > 0 {
		k.RegistryReplacement = transform.ParseOptionalFieldMapVal(extras["RegistryReplacement"])
	}
}

func (k KubernetesTransformPlugin) Run(u *unstructured.Unstructured, extras map[string]string) (transform.PluginResponse, error) {
	k.setOptionalFields(extras)
	resp := transform.PluginResponse{}
	// Set version in the future
	resp.Version = string(transform.V1)
	var err error
	resp.IsWhiteOut = k.getWhiteOuts(*u)
	if resp.IsWhiteOut {
		return resp, err
	}
	resp.Patches, err = k.getKubernetesTransforms(*u)
	return resp, err

}

func (k KubernetesTransformPlugin) Metadata() transform.PluginMetadata {
	return transform.PluginMetadata{
		Name:            "KubernetesPlugin",
		Version:         "v1",
		RequestVersion:  []transform.Version{transform.V1},
		ResponseVersion: []transform.Version{transform.V1},
		OptionalFields:  []transform.OptionalFields{
			{
				FlagName: "AddAnnotations",
				Help:     "Annotations to add to each resource",
				Example:  "annotation1=value1,annotation2=value2",
			},
			{
				FlagName: "RegistryReplacement",
				Help:     "Map of image registry paths to swap on transform, in the format original-registry1=target-registry1,original-registry2=target-registry2...",
				Example:  "docker-registry.default.svc:5000=image-registry.openshift-image-registry.svc:5000,docker.io/foo=quay.io/bar",
			},
			{
				FlagName: "NewNamespace",
				Help:     "Change the resource namespace to NewNamespace",
				Example:  "destination-namespace",
			},
			{
				FlagName: "RemoveAnnotations",
				Help:     "Annotations to remove",
				Example:  "annotation1,annotation2",
			},
		},
	}
}

var _ transform.Plugin = &KubernetesTransformPlugin{}

func (k KubernetesTransformPlugin) getWhiteOuts(obj unstructured.Unstructured) bool {
	groupKind := obj.GroupVersionKind().GroupKind()
	if groupKind == endpointGK {
		return true
	}

	if groupKind == endpointSliceGK {
		return true
	}

	// For right now we assume PVC's are handled by a different part
	// of the tool chain.
	if groupKind == pvcGK {
		return true
	}
	_, isPodSpecable := types.IsPodSpecable(obj)
	if (groupKind == podGK || isPodSpecable) && len(obj.GetOwnerReferences()) > 0 {
		return true
	}
	return false
}

func (k KubernetesTransformPlugin) getKubernetesTransforms(obj unstructured.Unstructured) (jsonpatch.Patch, error) {

	// Always attempt to add annotations for each thing.
	jsonPatch := jsonpatch.Patch{}
	if k.AddAnnotations != nil && len(k.AddAnnotations) > 0 {
		patches, err := addAnnotations(k.AddAnnotations)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patches...)
	}
	if len(k.RemoveAnnotations) > 0 {
		patches, err := removeAnnotations(k.RemoveAnnotations)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patches...)
	}
	if len(k.NewNamespace) > 0 {
		patches, err := updateNamespace(k.NewNamespace)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patches...)
	}
	if podGK == obj.GetObjectKind().GroupVersionKind().GroupKind() {
		patches, err := removePodFields()
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patches...)
	}
	if k.RegistryReplacement != nil && len(k.RegistryReplacement) > 0 {
		if podGK == obj.GetObjectKind().GroupVersionKind().GroupKind() {
			js, err := obj.MarshalJSON()
			if err != nil {
				return nil, err
			}
			pod := &v1.Pod{}
			err = json.Unmarshal(js, pod)
			if err != nil {
				return nil, err
			}
			jps := jsonpatch.Patch{}
			for i, container := range pod.Spec.Containers {
				updatedImage, update := updateImageRegistry(k.RegistryReplacement, container.Image)
				if update {
					jp, err := updateImage(fmt.Sprintf(podContainerImageUpdate, i), updatedImage)
					if err != nil {
						return nil, err
					}
					jps = append(jps, jp...)
				}
			}
			for i, container := range pod.Spec.InitContainers {
				updatedImage, update := updateImageRegistry(k.RegistryReplacement, container.Image)
				if update {
					jp, err := updateImage(fmt.Sprintf(podInitContainerImageUpdate, i), updatedImage)
					if err != nil {
						return nil, err
					}
					jps = append(jps, jp...)
				}
			}
			jsonPatch = append(jsonPatch, jps...)
		} else if template, ok := types.IsPodSpecable(obj); ok {
			jps := jsonpatch.Patch{}
			for i, container := range template.Spec.Containers {
				updatedImage, update := updateImageRegistry(k.RegistryReplacement, container.Image)
				if update {
					jp, err := updateImage(fmt.Sprintf(containerImageUpdate, i), updatedImage)
					if err != nil {
						return nil, err
					}
					jps = append(jps, jp...)
				}
			}
			for i, container := range template.Spec.InitContainers {
				updatedImage, update := updateImageRegistry(k.RegistryReplacement, container.Image)
				if update {
					jp, err := updateImage(fmt.Sprintf(initContainerImageUpdate, i), updatedImage)
					if err != nil {
						return nil, err
					}
					jps = append(jps, jp...)
				}
			}
			jsonPatch = append(jsonPatch, jps...)
		}
	}
	if obj.GetObjectKind().GroupVersionKind().GroupKind() == serviceGK {
		patches, err := removeServiceFields(obj)
		if err != nil {
			return nil, err
		}
		jsonPatch = append(jsonPatch, patches...)
	}

	return jsonPatch, nil
}

func updateImageRegistry(registryReplacements map[string]string, oldImageName string) (string, bool) {
	// Break up oldImage to get the registry URL. Assume all manifests are using fully qualified image paths, if not ignore.
	imageParts := strings.Split(oldImageName, "/")
	for i := len(imageParts); i > 0; i-- {
		if replacedImageParts, ok := registryReplacements[strings.Join(imageParts[:i], "/")]; ok {
			if i == len(imageParts) {
				return replacedImageParts, true
			}
			return fmt.Sprintf("%s/%s", replacedImageParts, strings.Join(imageParts[i:], "/")), true
		}
	}
	return "", false
}

func addAnnotations(addAnnotations map[string]string) (jsonpatch.Patch, error) {
	patchJSON := `[`
	i := 0
	for key, value := range addAnnotations {
		if i == 0 {
			patchJSON = fmt.Sprintf(annotationInitial, patchJSON, key, value)
		} else {
			patchJSON = fmt.Sprintf(annotationNext, patchJSON, key, value)
		}
		i++
	}

	patchJSON = fmt.Sprintf("%v]", patchJSON)
	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		fmt.Printf("%v", patchJSON)
		return nil, err
	}
	return patch, nil
}

func removeAnnotations(removeAnnotations []string) (jsonpatch.Patch, error) {
	patchJSON := `[`
	i := 0
	for _, annotation := range removeAnnotations {
		if i == 0 {
			patchJSON = fmt.Sprintf(removeAnnotationInitial, patchJSON, annotation)
		} else {
			patchJSON = fmt.Sprintf(removeAnnotationNext, patchJSON, annotation)
		}
		i++
	}

	patchJSON = fmt.Sprintf("%v]", patchJSON)
	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		fmt.Printf("%v", patchJSON)
		return nil, err
	}
	return patch, nil
}

func updateImage(containerImagePath, updatedImagePath string) (jsonpatch.Patch, error) {
	patchJSON := fmt.Sprintf(updateImageString, containerImagePath, updatedImagePath)

	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		return nil, err
	}
	return patch, nil
}

func removePodFields() (jsonpatch.Patch, error) {
	var patches jsonpatch.Patch
	patches, err := jsonpatch.DecodePatch([]byte(podNodeName))
	if err != nil {
		return nil, err
	}
	patch, err := jsonpatch.DecodePatch([]byte(podNodeSelector))
	if err != nil {
		return nil, err
	}
	patches = append(patches, patch...)
	patch, err = jsonpatch.DecodePatch([]byte(podPriority))
	if err != nil {
		return nil, err
	}
	patches = append(patches, patch...)
	return patches, nil
}

func updateNamespace(newNamespace string) (jsonpatch.Patch, error) {
	patchJSON := fmt.Sprintf(updateNamespaceString, newNamespace)

	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		return nil, err
	}
	return patch, nil
}

func updateRoleBindingSVCACCTNamespace(newNamespace string, numberOfSubjects int) (jsonpatch.Patch, error) {
	patchJSON := "["
	for i := 0; i < numberOfSubjects; i++ {
		if i != 0 {
			patchJSON = fmt.Sprintf("%v,", patchJSON)
		}
		patchJSON = fmt.Sprintf(updateRoleBindingSVCACCTNamspacestring, patchJSON, i, newNamespace)
	}

	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	if err != nil {
		return nil, err
	}
	return patch, nil
}

func removeServiceFields(obj unstructured.Unstructured) (jsonpatch.Patch, error) {
	var patches jsonpatch.Patch
	if isLoadBalancerService(obj) {
		patch, err := jsonpatch.DecodePatch([]byte(updateExternalIPs))
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch...)
	}

	if !isServiceClusterIPNone(obj) {
		patch, err := jsonpatch.DecodePatch([]byte(updateClusterIP))
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch...)
	}
	return patches, nil
}

func isLoadBalancerService(u unstructured.Unstructured) bool {
	// Get Spec
	spec, ok := u.UnstructuredContent()["spec"]
	if !ok {
		return false
	}

	specMap, ok := spec.(map[string]interface{})
	if !ok {
		return false
	}
	// Get type
	serviceType, ok := specMap["type"]
	if !ok {
		return false
	}
	return serviceType == "LoadBalancer"
}

func isServiceClusterIPNone(u unstructured.Unstructured) bool {
	// Get Spec
	spec, ok := u.UnstructuredContent()["spec"]
	if !ok {
		return false
	}

	specMap, ok := spec.(map[string]interface{})
	if !ok {
		return false
	}
	// Get type
	clusterIP, ok := specMap["clusterIP"]
	if !ok {
		return false
	}
	return clusterIP == "None"
}
