package controller

import (
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// SkipDeletion returns a predicate that filters out Delete events. Use
// this on watches where deletion is handled via the deletion-timestamp
// path (i.e., a finalizer is in play) — in that case the controller
// observes the deletion as an Update with non-zero
// metadata.deletionTimestamp, and the synthesized Delete event that
// fires after finalizer removal is uninteresting.
func SkipDeletion() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
	}
}

// HasAnnotation returns a predicate that admits an event only if the
// involved object's metadata.annotations carry `key`. Useful for
// gating reconciles on opt-in annotations (e.g. "myorg.dev/managed").
func HasAnnotation(key string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		ann := obj.GetAnnotations()
		if ann == nil {
			return false
		}
		_, ok := ann[key]
		return ok
	})
}

// HasLabel returns a predicate that admits an event only if the
// involved object's metadata.labels match selector.
func HasLabel(selector labels.Selector) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return selector.Matches(labels.Set(obj.GetLabels()))
	})
}

// AnnotationChanged returns a predicate that fires on Update events
// where the named annotation's value differs between old and new
// objects, and on Create events where the annotation is present.
// Delete and Generic events pass through unchanged.
func AnnotationChanged(key string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ann := e.Object.GetAnnotations()
			_, ok := ann[key]
			return ok
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldVal := ""
			newVal := ""
			if a := e.ObjectOld.GetAnnotations(); a != nil {
				oldVal = a[key]
			}
			if a := e.ObjectNew.GetAnnotations(); a != nil {
				newVal = a[key]
			}
			return oldVal != newVal
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
	}
}
