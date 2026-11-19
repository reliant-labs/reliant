package stsresize

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

// template is a volumeClaimTemplate as a render declares it: no volumeMode,
// no status — the undefaulted shape.
func template(name, size string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr("standard-rwo"),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}

// storedTemplate is the same template as the API server returns it: volumeMode
// defaulted, status present.
func storedTemplate(name, size string) corev1.PersistentVolumeClaim {
	t := template(name, size)
	t.Spec.VolumeMode = ptr(corev1.PersistentVolumeFilesystem)
	t.Status.Phase = corev1.ClaimPending
	return t
}

func sts(templates ...corev1.PersistentVolumeClaim) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse", Namespace: "observability-data"},
		Spec: appsv1.StatefulSetSpec{
			Replicas:             ptr(int32(1)),
			ServiceName:          "clickhouse",
			Selector:             &metav1.LabelSelector{MatchLabels: map[string]string{"app": "clickhouse"}},
			VolumeClaimTemplates: templates,
		},
	}
}

func claim(name, size string, class *string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "observability-data"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: class,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}

func class(name string, expandable *bool, isDefault bool) storagev1.StorageClass {
	sc := storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, AllowVolumeExpansion: expandable}
	if isDefault {
		sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return sc
}

var expandable = []storagev1.StorageClass{class("standard-rwo", ptr(true), true)}

func claimNames(p Plan) []string {
	var out []string
	for _, r := range p.Resize {
		out = append(out, r.Name)
	}
	return out
}

func TestDecide_GrowthResizesEveryOrdinalAndRecreates(t *testing.T) {
	claims := []corev1.PersistentVolumeClaim{
		claim("data-clickhouse-0", "50Gi", ptr("standard-rwo")),
		claim("data-clickhouse-1", "50Gi", ptr("standard-rwo")),
		// A replica scaled away keeps its claim; a later scale-up must not
		// bring back a small volume.
		claim("data-clickhouse-7", "50Gi", ptr("standard-rwo")),
		// Not this StatefulSet's claims.
		claim("data-clickhouse-backup-0", "50Gi", ptr("standard-rwo")),
		claim("data-other-0", "50Gi", ptr("standard-rwo")),
	}
	p, err := Decide(sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")), claims, expandable)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Recreate {
		t.Fatal("a grown template must recreate the StatefulSet")
	}
	if got, want := claimNames(p), []string{"data-clickhouse-0", "data-clickhouse-1", "data-clickhouse-7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resize = %v, want %v", got, want)
	}
	if p.Resize[0].To.String() != "100Gi" || p.Resize[0].From.String() != "50Gi" {
		t.Errorf("resize[0] = %+v", p.Resize[0])
	}
	if !strings.Contains(p.String(), "data 50Gi→100Gi") {
		t.Errorf("String() = %q", p.String())
	}
}

// The prod case this package was written for: the claim was expanded by hand
// ahead of the declaration. Nothing to grow, but the object still has to be
// recreated so the template matches.
func TestDecide_ClaimAlreadyExpandedStillRecreates(t *testing.T) {
	claims := []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "100Gi", ptr("standard-rwo"))}
	p, err := Decide(sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")), claims, expandable)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Recreate || len(p.Resize) != 0 {
		t.Fatalf("plan = %+v, want recreate with no claim to grow", p)
	}
}

// A claim larger than the declaration is left alone, never shrunk, and needs
// no expandable class.
func TestDecide_ClaimLargerThanDeclarationIsKept(t *testing.T) {
	claims := []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "200Gi", ptr("static"))}
	p, err := Decide(sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")), claims, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Resize) != 0 {
		t.Fatalf("resize = %v, want none", claimNames(p))
	}
}

func TestDecide_UnchangedIsTheZeroPlan(t *testing.T) {
	p, err := Decide(sts(template("data", "50Gi")), sts(storedTemplate("data", "50Gi")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Recreate || len(p.Resize) != 0 {
		t.Fatalf("plan = %+v, want nothing to do: the server's defaults are not a change", p)
	}
}

func TestDecide_AbsentLiveIsTheZeroPlan(t *testing.T) {
	p, err := Decide(sts(template("data", "50Gi")), nil, nil, nil)
	if err != nil || p.Recreate {
		t.Fatalf("plan = %+v, err = %v", p, err)
	}
}

func TestDecide_Refusals(t *testing.T) {
	changedClass := template("data", "100Gi")
	changedClass.Spec.StorageClassName = ptr("premium-rwo")
	changedMode := template("data", "100Gi")
	changedMode.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	changedLabel := template("data", "100Gi")
	changedLabel.Labels = map[string]string{"tier": "hot"}

	cases := []struct {
		name    string
		desired *appsv1.StatefulSet
		live    *appsv1.StatefulSet
		claims  []corev1.PersistentVolumeClaim
		classes []storagev1.StorageClass
		want    error
	}{
		{"shrink", sts(template("data", "20Gi")), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrShrink},
		{"class changed", sts(changedClass), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrTemplateChanged},
		{"access mode changed", sts(changedMode), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrTemplateChanged},
		{"label changed", sts(changedLabel), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrTemplateChanged},
		{"template added", sts(template("data", "50Gi"), template("wal", "5Gi")), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrTemplateChanged},
		{"template renamed", sts(template("store", "100Gi")), sts(storedTemplate("data", "50Gi")), nil, expandable, ErrTemplateChanged},
		{
			"class not expandable", sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")),
			[]corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))},
			[]storagev1.StorageClass{class("standard-rwo", ptr(false), true)}, ErrNotExpandable,
		},
		{
			"class expansion unset", sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")),
			[]corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))},
			[]storagev1.StorageClass{class("standard-rwo", nil, true)}, ErrNotExpandable,
		},
		{
			"class missing", sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")),
			[]corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("deleted-class"))},
			expandable, ErrNotExpandable,
		},
		{
			"statically bound claim", sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")),
			[]corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr(""))},
			expandable, ErrNotExpandable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Decide(tc.desired, tc.live, tc.claims, tc.classes)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if p.Recreate {
				t.Fatal("a refusal must not plan a recreate")
			}
		})
	}
}

// A claim that names no class was provisioned from the cluster default.
func TestDecide_ClaimWithoutClassUsesTheDefault(t *testing.T) {
	claims := []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", nil)}
	classes := []storagev1.StorageClass{class("slow", ptr(false), false), class("standard-rwo", ptr(true), true)}
	p, err := Decide(sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi")), claims, classes)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Resize) != 1 || p.Resize[0].StorageClass != "standard-rwo" {
		t.Fatalf("resize = %+v", p.Resize)
	}
}

// fakeCluster records the writes Converge makes, in order.
type fakeCluster struct {
	live    *appsv1.StatefulSet
	claims  []corev1.PersistentVolumeClaim
	classes []storagev1.StorageClass
	dryRun  error
	resize  error
	orphan  error

	dryRunGot *appsv1.StatefulSet
	writes    []string
}

func (f *fakeCluster) StatefulSet(context.Context, string, string) (*appsv1.StatefulSet, error) {
	return f.live, nil
}

func (f *fakeCluster) PersistentVolumeClaims(context.Context, string) ([]corev1.PersistentVolumeClaim, error) {
	return f.claims, nil
}

func (f *fakeCluster) StorageClasses(context.Context) ([]storagev1.StorageClass, error) {
	return f.classes, nil
}

func (f *fakeCluster) DryRunApply(_ context.Context, s *appsv1.StatefulSet) error {
	f.dryRunGot = s
	return f.dryRun
}

func (f *fakeCluster) ResizeClaim(_ context.Context, ns, name string, size resource.Quantity) error {
	f.writes = append(f.writes, "resize "+ns+"/"+name+" "+size.String())
	return f.resize
}

func (f *fakeCluster) OrphanStatefulSet(_ context.Context, ns, name string) error {
	f.writes = append(f.writes, "orphan "+ns+"/"+name)
	return f.orphan
}

func TestConverge_GrowsClaimsThenOrphansTheStatefulSet(t *testing.T) {
	f := &fakeCluster{
		live: sts(storedTemplate("data", "50Gi")),
		claims: []corev1.PersistentVolumeClaim{
			claim("data-clickhouse-1", "50Gi", ptr("standard-rwo")),
			claim("data-clickhouse-0", "50Gi", ptr("standard-rwo")),
		},
		classes: expandable,
	}
	desired := sts(template("data", "100Gi"))
	p, err := Converge(context.Background(), f, desired)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"resize observability-data/data-clickhouse-0 100Gi",
		"resize observability-data/data-clickhouse-1 100Gi",
		"orphan observability-data/clickhouse",
	}
	if !reflect.DeepEqual(f.writes, want) {
		t.Fatalf("writes = %v, want %v", f.writes, want)
	}
	if !p.Recreate {
		t.Fatal("plan must report the recreate")
	}
	// The dry run carries the LIVE templates, so it asks the server about
	// every other field and nothing else.
	if got := f.dryRunGot.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "50Gi" {
		t.Errorf("dry run template size = %s, want the live 50Gi", got.String())
	}
	if got := desired.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]; got.String() != "100Gi" {
		t.Errorf("Converge mutated the caller's desired object: %s", got.String())
	}
}

func TestConverge_RefusesWhenAnotherImmutableFieldChanged(t *testing.T) {
	f := &fakeCluster{
		live:    sts(storedTemplate("data", "50Gi")),
		claims:  []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))},
		classes: expandable,
		dryRun:  errors.New(`The StatefulSet "clickhouse" is invalid: spec.selector: Invalid value: field is immutable`),
	}
	_, err := Converge(context.Background(), f, sts(template("data", "100Gi")))
	if !errors.Is(err, ErrOtherImmutable) {
		t.Fatalf("err = %v, want ErrOtherImmutable", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("a refusal wrote %v", f.writes)
	}
}

func TestConverge_NothingToDoWritesNothing(t *testing.T) {
	f := &fakeCluster{live: sts(storedTemplate("data", "50Gi")), classes: expandable}
	p, err := Converge(context.Background(), f, sts(template("data", "50Gi")))
	if err != nil || p.Recreate || len(f.writes) != 0 || f.dryRunGot != nil {
		t.Fatalf("plan=%+v err=%v writes=%v dryRun=%v", p, err, f.writes, f.dryRunGot != nil)
	}
	f = &fakeCluster{}
	p, err = Converge(context.Background(), f, sts(template("data", "50Gi")))
	if err != nil || p.Recreate || len(f.writes) != 0 {
		t.Fatalf("absent StatefulSet: plan=%+v err=%v writes=%v", p, err, f.writes)
	}
}

func TestConverge_ClaimFailureLeavesTheStatefulSet(t *testing.T) {
	f := &fakeCluster{
		live:    sts(storedTemplate("data", "50Gi")),
		claims:  []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))},
		classes: expandable,
		resize:  errors.New("quota exceeded"),
	}
	if _, err := Converge(context.Background(), f, sts(template("data", "100Gi"))); err == nil {
		t.Fatal("want the resize error")
	}
	for _, w := range f.writes {
		if strings.HasPrefix(w, "orphan") {
			t.Fatalf("the StatefulSet was deleted after a claim failed to grow: %v", f.writes)
		}
	}
}

func TestConverge_RefusalWritesNothing(t *testing.T) {
	f := &fakeCluster{live: sts(storedTemplate("data", "50Gi")), classes: expandable}
	if _, err := Converge(context.Background(), f, sts(template("data", "10Gi"))); !errors.Is(err, ErrShrink) {
		t.Fatalf("err = %v, want ErrShrink", err)
	}
	if len(f.writes) != 0 || f.dryRunGot != nil {
		t.Fatalf("writes = %v", f.writes)
	}
}

func TestConverge_RequiresANamespace(t *testing.T) {
	d := sts(template("data", "100Gi"))
	d.Namespace = ""
	if _, err := Converge(context.Background(), &fakeCluster{}, d); err == nil {
		t.Fatal("a namespace-less desired object must be refused, not resolved to some default")
	}
}
