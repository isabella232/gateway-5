// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gatewayapi

import (
	"reflect"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/gateway-api/apis/v1alpha2"
	"sigs.k8s.io/gateway-api/apis/v1beta1"

	egv1alpha1 "github.com/envoyproxy/gateway/api/config/v1alpha1"
)

// GatewayContext wraps a Gateway and provides helper methods for
// setting conditions, accessing Listeners, etc.
type GatewayContext struct {
	*v1beta1.Gateway

	listeners []*ListenerContext
}

// GetListenerContext returns the ListenerContext with listenerName.
// If the listener exists in the Gateway Spec but NOT yet in the GatewayContext,
// this creates a new ListenerContext for the listener and attaches it to the
// GatewayContext.
func (g *GatewayContext) GetListenerContext(listenerName v1beta1.SectionName) *ListenerContext {
	if g.listeners == nil {
		g.listeners = make([]*ListenerContext, 0)
	}

	for _, l := range g.listeners {
		if l.Name == listenerName {
			return l
		}
	}

	var listener *v1beta1.Listener
	for i, l := range g.Spec.Listeners {
		if l.Name == listenerName {
			listener = &g.Spec.Listeners[i]
			break
		}
	}
	if listener == nil {
		panic("listener not found")
	}

	listenerStatusIdx := -1
	for i := range g.Status.Listeners {
		if g.Status.Listeners[i].Name == listenerName {
			listenerStatusIdx = i
			break
		}
	}
	if listenerStatusIdx == -1 {
		g.Status.Listeners = append(g.Status.Listeners, v1beta1.ListenerStatus{Name: listenerName})
		listenerStatusIdx = len(g.Status.Listeners) - 1
	}

	ctx := &ListenerContext{
		Listener:          listener,
		gateway:           g.Gateway,
		listenerStatusIdx: listenerStatusIdx,
	}
	g.listeners = append(g.listeners, ctx)
	return ctx
}

// ListenerContext wraps a Listener and provides helper methods for
// setting conditions and other status information on the associated
// Gateway, etc.
type ListenerContext struct {
	*v1beta1.Listener

	gateway           *v1beta1.Gateway
	listenerStatusIdx int
	namespaceSelector labels.Selector
	tlsSecret         *v1.Secret
}

func (l *ListenerContext) SetCondition(conditionType v1beta1.ListenerConditionType, status metav1.ConditionStatus, reason v1beta1.ListenerConditionReason, message string) {
	cond := metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: l.gateway.Generation,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	idx := -1
	for i, existing := range l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions {
		if existing.Type == cond.Type {
			// return early if the condition is unchanged
			if existing.Status == cond.Status &&
				existing.Reason == cond.Reason &&
				existing.Message == cond.Message {
				return
			}
			idx = i
			break
		}
	}

	if idx > -1 {
		l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions[idx] = cond
	} else {
		l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions = append(l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions, cond)
	}
}

func (l *ListenerContext) ResetConditions() {
	l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions = make([]metav1.Condition, 0)
}

func (l *ListenerContext) SetSupportedKinds(kinds ...v1beta1.RouteGroupKind) {
	l.gateway.Status.Listeners[l.listenerStatusIdx].SupportedKinds = kinds
}

func (l *ListenerContext) ResetAttachedRoutes() {
	// Reset attached route count since it will be recomputed during translation.
	l.gateway.Status.Listeners[l.listenerStatusIdx].AttachedRoutes = 0
}

func (l *ListenerContext) IncrementAttachedRoutes() {
	l.gateway.Status.Listeners[l.listenerStatusIdx].AttachedRoutes++
}

func (l *ListenerContext) AllowsKind(kind v1beta1.RouteGroupKind) bool {
	for _, allowed := range l.gateway.Status.Listeners[l.listenerStatusIdx].SupportedKinds {
		if GroupDerefOr(allowed.Group, "") == GroupDerefOr(kind.Group, "") && allowed.Kind == kind.Kind {
			return true
		}
	}

	return false
}

func (l *ListenerContext) AllowsNamespace(namespace *v1.Namespace) bool {
	if namespace == nil {
		return false
	}
	switch *l.AllowedRoutes.Namespaces.From {
	case v1beta1.NamespacesFromAll:
		return true
	case v1beta1.NamespacesFromSelector:
		if l.namespaceSelector == nil {
			return false
		}
		return l.namespaceSelector.Matches(labels.Set(namespace.Labels))
	default:
		// NamespacesFromSame is the default
		return l.gateway.Namespace == namespace.Name
	}
}

func (l *ListenerContext) IsReady() bool {
	for _, cond := range l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions {
		if cond.Type == string(v1beta1.ListenerConditionReady) && cond.Status == metav1.ConditionTrue {
			return true
		}
	}

	return false
}

func (l *ListenerContext) GetConditions() []metav1.Condition {
	return l.gateway.Status.Listeners[l.listenerStatusIdx].Conditions
}

func (l *ListenerContext) SetTLSSecret(tlsSecret *v1.Secret) {
	l.tlsSecret = tlsSecret
}

// RouteContext represents a generic Route object (HTTPRoute, TLSRoute, etc.)
// that can reference Gateway objects.
type RouteContext interface {
	client.Object

	// GetRouteType returns the Kind of the Route object, HTTPRoute,
	// TLSRoute, TCPRoute, UDPRoute etc.
	GetRouteType() string

	// TODO: [v1alpha2-v1beta1] This should not be required once all Route
	// objects being implemented are of type v1beta1.
	// GetHostnames returns the hosts targeted by the Route object.
	GetHostnames() []string

	// TODO: [v1alpha2-v1beta1] This should not be required once all Route
	// objects being implemented are of type v1beta1.
	// GetParentReferences returns the ParentReference of the Route object.
	GetParentReferences() []v1beta1.ParentReference

	// GetRouteParentContext returns RouteParentContext by using the Route
	// objects' ParentReference.
	GetRouteParentContext(forParentRef v1beta1.ParentReference) *RouteParentContext
}

// HTTPRouteContext wraps an HTTPRoute and provides helper methods for
// accessing the route's parents.
type HTTPRouteContext struct {
	*v1beta1.HTTPRoute

	parentRefs map[v1beta1.ParentReference]*RouteParentContext
}

func (h *HTTPRouteContext) GetRouteType() string {
	return KindHTTPRoute
}

func (h *HTTPRouteContext) GetHostnames() []string {
	hostnames := make([]string, len(h.Spec.Hostnames))
	for idx, s := range h.Spec.Hostnames {
		hostnames[idx] = string(s)
	}
	return hostnames
}

func (h *HTTPRouteContext) GetParentReferences() []v1beta1.ParentReference {
	return h.Spec.ParentRefs
}

func (h *HTTPRouteContext) GetRouteParentContext(forParentRef v1beta1.ParentReference) *RouteParentContext {
	if h.parentRefs == nil {
		h.parentRefs = make(map[v1beta1.ParentReference]*RouteParentContext)
	}

	if ctx := h.parentRefs[forParentRef]; ctx != nil {
		return ctx
	}

	var parentRef *v1beta1.ParentReference
	for i, p := range h.Spec.ParentRefs {
		if reflect.DeepEqual(p, forParentRef) {
			parentRef = &h.Spec.ParentRefs[i]
			break
		}
	}
	if parentRef == nil {
		panic("parentRef not found")
	}

	routeParentStatusIdx := -1
	for i := range h.Status.Parents {
		if reflect.DeepEqual(h.Status.Parents[i].ParentRef, forParentRef) {
			routeParentStatusIdx = i
			break
		}
	}
	if routeParentStatusIdx == -1 {
		rParentStatus := v1beta1.RouteParentStatus{
			// TODO: get this value from the config
			ControllerName: v1beta1.GatewayController(egv1alpha1.GatewayControllerName),
			ParentRef:      forParentRef,
		}
		h.Status.Parents = append(h.Status.Parents, rParentStatus)
		routeParentStatusIdx = len(h.Status.Parents) - 1
	}

	ctx := &RouteParentContext{
		ParentReference: parentRef,

		httpRoute:            h.HTTPRoute,
		routeParentStatusIdx: routeParentStatusIdx,
	}
	h.parentRefs[forParentRef] = ctx
	return ctx
}

// TLSRouteContext wraps a TLSRoute and provides helper methods for
// accessing the route's parents.
type TLSRouteContext struct {
	*v1alpha2.TLSRoute

	parentRefs map[v1beta1.ParentReference]*RouteParentContext
}

func (t *TLSRouteContext) GetRouteType() string {
	return KindTLSRoute
}

func (t *TLSRouteContext) GetHostnames() []string {
	hostnames := make([]string, len(t.Spec.Hostnames))
	for idx, s := range t.Spec.Hostnames {
		hostnames[idx] = string(s)
	}
	return hostnames
}

func (t *TLSRouteContext) GetParentReferences() []v1beta1.ParentReference {
	parentReferences := make([]v1beta1.ParentReference, len(t.Spec.ParentRefs))
	for idx, p := range t.Spec.ParentRefs {
		parentReferences[idx] = UpgradeParentReference(p)
	}
	return parentReferences
}

func (t *TLSRouteContext) GetRouteParentContext(forParentRef v1beta1.ParentReference) *RouteParentContext {
	if t.parentRefs == nil {
		t.parentRefs = make(map[v1beta1.ParentReference]*RouteParentContext)
	}

	if ctx := t.parentRefs[forParentRef]; ctx != nil {
		return ctx
	}

	var parentRef *v1beta1.ParentReference
	for i, p := range t.Spec.ParentRefs {
		p := UpgradeParentReference(p)
		if reflect.DeepEqual(p, forParentRef) {
			upgraded := UpgradeParentReference(t.Spec.ParentRefs[i])
			parentRef = &upgraded
			break
		}
	}
	if parentRef == nil {
		panic("parentRef not found")
	}

	routeParentStatusIdx := -1
	for i := range t.Status.Parents {
		p := UpgradeParentReference(t.Status.Parents[i].ParentRef)
		defaultNamespace := v1beta1.Namespace(metav1.NamespaceDefault)
		if forParentRef.Namespace == nil {
			forParentRef.Namespace = &defaultNamespace
		}
		if p.Namespace == nil {
			p.Namespace = &defaultNamespace
		}
		if reflect.DeepEqual(p, forParentRef) {
			routeParentStatusIdx = i
			break
		}
	}
	if routeParentStatusIdx == -1 {
		rParentStatus := v1alpha2.RouteParentStatus{
			// TODO: get this value from the config
			ControllerName: v1alpha2.GatewayController(egv1alpha1.GatewayControllerName),
			ParentRef:      DowngradeParentReference(forParentRef),
		}
		t.Status.Parents = append(t.Status.Parents, rParentStatus)
		routeParentStatusIdx = len(t.Status.Parents) - 1
	}

	ctx := &RouteParentContext{
		ParentReference: parentRef,

		tlsRoute:             t.TLSRoute,
		routeParentStatusIdx: routeParentStatusIdx,
	}
	t.parentRefs[forParentRef] = ctx
	return ctx
}

// RouteParentContext wraps a ParentReference and provides helper methods for
// setting conditions and other status information on the associated
// HTTPRoute, TLSRoute etc.
type RouteParentContext struct {
	*v1beta1.ParentReference

	// TODO: [v1alpha2-v1beta1] This can probably be replaced with
	// a single field pointing to *v1beta1.RouteStatus.
	httpRoute *v1beta1.HTTPRoute
	tlsRoute  *v1alpha2.TLSRoute

	routeParentStatusIdx int
	listeners            []*ListenerContext
}

func (r *RouteParentContext) SetListeners(listeners ...*ListenerContext) {
	r.listeners = append(r.listeners, listeners...)
}

func (r *RouteParentContext) SetCondition(route RouteContext, conditionType v1beta1.RouteConditionType, status metav1.ConditionStatus, reason v1beta1.RouteConditionReason, message string) {
	cond := metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: route.GetGeneration(),
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	idx := -1
	switch route.GetRouteType() {
	case KindHTTPRoute:
		for i, existing := range r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions {
			if existing.Type == cond.Type {
				// return early if the condition is unchanged
				if existing.Status == cond.Status &&
					existing.Reason == cond.Reason &&
					existing.Message == cond.Message {
					return
				}
				idx = i
				break
			}
		}

		if idx > -1 {
			r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions[idx] = cond
		} else {
			r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions = append(r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions, cond)
		}
	case KindTLSRoute:
		for i, existing := range r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions {
			if existing.Type == cond.Type {
				// return early if the condition is unchanged
				if existing.Status == cond.Status &&
					existing.Reason == cond.Reason &&
					existing.Message == cond.Message {
					return
				}
				idx = i
				break
			}
		}

		if idx > -1 {
			r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions[idx] = cond
		} else {
			r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions = append(r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions, cond)
		}
	}
}

func (r *RouteParentContext) ResetConditions(route RouteContext) {
	switch route.GetRouteType() {
	case KindHTTPRoute:
		r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions = make([]metav1.Condition, 0)
	case KindTLSRoute:
		r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions = make([]metav1.Condition, 0)
	}
}

func (r *RouteParentContext) IsAccepted(route RouteContext) bool {
	var conditions []metav1.Condition
	switch route.GetRouteType() {
	case KindHTTPRoute:
		conditions = r.httpRoute.Status.Parents[r.routeParentStatusIdx].Conditions
	case KindTLSRoute:
		conditions = r.tlsRoute.Status.Parents[r.routeParentStatusIdx].Conditions
	}
	for _, cond := range conditions {
		if cond.Type == string(v1beta1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
			return true
		}
	}

	return false
}
