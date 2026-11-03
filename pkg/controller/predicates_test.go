package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestSkipDeletion(t *testing.T) {
	p := SkipDeletion()
	if !p.Create(event.CreateEvent{}) {
		t.Error("Create should pass")
	}
	if !p.Update(event.UpdateEvent{}) {
		t.Error("Update should pass")
	}
	if !p.Generic(event.GenericEvent{}) {
		t.Error("Generic should pass")
	}
	if p.Delete(event.DeleteEvent{}) {
		t.Error("Delete should be filtered")
	}
}

func TestHasAnnotation(t *testing.T) {
	p := HasAnnotation("forge.dev/managed")

	withAnn := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"forge.dev/managed": "true"},
		},
	}
	withoutAnn := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{}}

	if !p.Create(event.CreateEvent{Object: withAnn}) {
		t.Error("with annotation: Create should pass")
	}
	if p.Create(event.CreateEvent{Object: withoutAnn}) {
		t.Error("without annotation: Create should be filtered")
	}
}

func TestHasLabel(t *testing.T) {
	sel := labels.SelectorFromSet(labels.Set{"app": "reliant"})
	p := HasLabel(sel)

	withLabel := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{"app": "reliant"},
	}}
	wrongLabel := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{"app": "other"},
	}}
	noLabel := &corev1.Pod{}

	if !p.Create(event.CreateEvent{Object: withLabel}) {
		t.Error("matching label: Create should pass")
	}
	if p.Create(event.CreateEvent{Object: wrongLabel}) {
		t.Error("wrong label: Create should be filtered")
	}
	if p.Create(event.CreateEvent{Object: noLabel}) {
		t.Error("no label: Create should be filtered")
	}
}

func TestAnnotationChanged(t *testing.T) {
	p := AnnotationChanged("forge.dev/version")

	a := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"forge.dev/version": "v1"},
	}}
	b := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"forge.dev/version": "v2"},
	}}
	same := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"forge.dev/version": "v1"},
	}}

	if !p.Update(event.UpdateEvent{ObjectOld: a, ObjectNew: b}) {
		t.Error("changed annotation: Update should pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: a, ObjectNew: same}) {
		t.Error("unchanged annotation: Update should be filtered")
	}
	if !p.Create(event.CreateEvent{Object: a}) {
		t.Error("Create with annotation: should pass")
	}
	if p.Create(event.CreateEvent{Object: &corev1.Pod{}}) {
		t.Error("Create without annotation: should be filtered")
	}
}
