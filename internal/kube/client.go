// Package kube builds the Kubernetes clients khook uses: a dynamic client
// for arbitrary manifests, a typed clientset for well-known operations, and
// a shortcut-expanding RESTMapper for resolving resource arguments like
// "pods" or "daemonset/aws-node".
package kube

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Clients bundles everything the executors need to talk to one cluster.
type Clients struct {
	Dynamic dynamic.Interface
	Typed   kubernetes.Interface
	Mapper  meta.RESTMapper

	kubeconfig  string
	kubecontext string
}

// New builds clients from a kubeconfig path and context name; empty values
// fall back to the standard loading rules (KUBECONFIG, ~/.kube/config).
func New(kubeconfig, kubecontext string) (*Clients, error) {
	flags := configFlags(kubeconfig, kubecontext, "")
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("building clientset: %w", err)
	}
	// cli-runtime returns a deferred, shortcut-expanding mapper, so "po",
	// "pods", and "Pod" all resolve.
	mapper, err := flags.ToRESTMapper()
	if err != nil {
		return nil, fmt.Errorf("building REST mapper: %w", err)
	}
	return &Clients{
		Dynamic:     dyn,
		Typed:       typed,
		Mapper:      mapper,
		kubeconfig:  kubeconfig,
		kubecontext: kubecontext,
	}, nil
}

// HelmGetter returns a RESTClientGetter for the Helm SDK, scoped to a
// namespace.
func (c *Clients) HelmGetter(namespace string) genericclioptions.RESTClientGetter {
	return configFlags(c.kubeconfig, c.kubecontext, namespace)
}

func configFlags(kubeconfig, kubecontext, namespace string) *genericclioptions.ConfigFlags {
	flags := genericclioptions.NewConfigFlags(false)
	if kubeconfig != "" {
		flags.KubeConfig = &kubeconfig
	}
	if kubecontext != "" {
		flags.Context = &kubecontext
	}
	if namespace != "" {
		flags.Namespace = &namespace
	}
	return flags
}

// ResolvedResource is a resource argument mapped to something addressable.
type ResolvedResource struct {
	GVR        schema.GroupVersionResource
	Namespaced bool
}

// ResolveResourceArg maps a type argument ("pods", "po", "daemonset",
// "deployments.apps") to a GVR plus scope using the shortcut-expanding
// mapper.
func (c *Clients) ResolveResourceArg(arg string) (*ResolvedResource, error) {
	gvr, groupResource := schema.ParseResourceArg(strings.ToLower(arg))
	if gvr == nil {
		gvr = &schema.GroupVersionResource{Group: groupResource.Group, Resource: groupResource.Resource}
	}
	full, err := c.Mapper.ResourceFor(*gvr)
	if err != nil {
		return nil, fmt.Errorf("unknown resource type %q: %w", arg, err)
	}
	gvk, err := c.Mapper.KindFor(full)
	if err != nil {
		return nil, fmt.Errorf("resolving kind for %q: %w", arg, err)
	}
	mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving scope for %q: %w", arg, err)
	}
	return &ResolvedResource{
		GVR:        full,
		Namespaced: mapping.Scope.Name() == meta.RESTScopeNameNamespace,
	}, nil
}
