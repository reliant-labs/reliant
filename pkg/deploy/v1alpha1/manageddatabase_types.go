package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Engine is a database engine. A closed set with one member: the tier
// provisions CloudNativePG Clusters, and CNPG runs Postgres. The field exists
// so that adding an engine later widens an enum instead of migrating a
// resource that already has rows.
//
// +kubebuilder:validation:Enum=postgres
type Engine string

const EnginePostgres Engine = "postgres"

// DeletionPolicy decides what deleting the resource does to the DATA.
//
// THE DEFAULT IS RETAIN, and it is the most consequential default on this
// type. A resource is configuration and can be deleted by a kubectl delete, a
// pruning GitOps sync, a namespace teardown or a typo. None of those are
// statements about whether the data should continue to exist.
//
// +kubebuilder:validation:Enum=retain;delete
type DeletionPolicy string

const (
	// DeletionPolicyRetain leaves the Cluster and its volumes in place,
	// unowned. The data survives.
	DeletionPolicyRetain DeletionPolicy = "retain"
	// DeletionPolicyDelete deletes the Cluster and therefore its volumes.
	// This is irreversible, and it is only ever reached because someone
	// typed it.
	DeletionPolicyDelete DeletionPolicy = "delete"
)

// ManagedDatabaseSpec is a dedicated Postgres, rendered as a CloudNativePG
// Cluster in the workload's own namespace.
//
// MERGE DECISIONS. control-plane's type wins, because forge had no schema.
// It ran live (the `threetier` database, Ready, backing a backend through
// DatabaseRef). Two changes on merit:
//
//   - StorageGiB, not control-plane's StorageGB. The value was always
//     rendered as "<n>Gi", so the field name claimed decimal gigabytes
//     while the volume was sized in binary GiB. The units now agree with
//     Workload.StorageGiB and with what is actually provisioned.
//   - OrgID, EnvironmentID, DeploymentID and Namespace are gone from the
//     spec: identity is labels and the namespace is the target. The
//     database's name is metadata.name. control-plane's DatabaseName field
//     duplicated it, and the two could disagree.
//
// SCALARS AND CLOSED ENUMS ONLY. There is no postgresql.conf passthrough, no
// extension list, no version selector, no backup stanza and no raw CNPG
// object. A field the tier would have to REJECT is a field it does not
// declare.
type ManagedDatabaseSpec struct {
	// Engine is the database engine. Default postgres.
	// +optional
	// +kubebuilder:default=postgres
	Engine Engine `json:"engine,omitempty"`

	// Instances is how many Postgres instances run: one primary plus
	// (Instances-1) hot standbys. Default 1, ceiling MaxDatabaseInstances.
	// A standby buys failover, not durability.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3
	Instances int32 `json:"instances,omitempty"`

	// StorageGiB sizes EACH instance's volume, so the cluster provisions
	// Instances × StorageGiB. Default DefaultDatabaseStorageGiB.
	// +optional
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=500
	StorageGiB int32 `json:"storageGiB,omitempty"`

	// DeletionPolicy is what deleting this resource does to the data.
	// Default retain.
	// +optional
	// +kubebuilder:default=retain
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Restore, when set, bootstraps this database from a backup instead of
	// from an empty initdb. DECLARING it is the whole restore procedure:
	// there is no imperative RPC, because a restore that only exists as a
	// command is one nobody can review, repeat, or run at 3am from a git
	// revert.
	//
	// It is honoured ONCE, at bootstrap. A Cluster that already holds data
	// is never re-bootstrapped, so adding this to a live database does
	// nothing — restoring over a running database means declaring a NEW
	// one that names the old as its source, which is also what keeps a
	// mistaken restore from destroying the original.
	// +optional
	Restore *DatabaseRestore `json:"restore,omitempty"`
}

// DatabaseRestore is a declarative recovery: which database's backups to
// read, and optionally how far to replay them.
//
// WHY SourceDatabase IS NOT OPTIONAL. Defaulting it to "this database" would
// make the common typo — declaring a restore on the database you meant to
// restore INTO, with the source left blank — silently mean "recover myself
// from my own backups", which is indistinguishable from a working restore
// until you look for the data that is missing.
type DatabaseRestore struct {
	// SourceDatabase is the ManagedDatabase whose backups are read. It may
	// be this database's own name (recreating it in place) or another in
	// the same environment — never one in another environment, which the
	// platform enforces by scoping the object-store prefix, not by
	// checking a string here.
	// +kubebuilder:validation:MinLength=1
	SourceDatabase string `json:"sourceDatabase"`

	// PointInTime stops recovery at an RFC3339 timestamp instead of
	// replaying every archived WAL. Unset recovers as far as the archive
	// goes.
	//
	// This is the field that makes the difference between "we have
	// backups" and "we can undo what happened at 14:05", and it is the
	// only reason continuous WAL archiving is worth its cost.
	// +optional
	PointInTime string `json:"pointInTime,omitempty"`
}

// Defaults and ceilings. THE CEILING IS THE POINT: a default only helps the
// author who declared nothing, while a declaration of 50 instances describes a
// capacity event.
const (
	DefaultDatabaseInstances  int32 = 1
	MaxDatabaseInstances      int32 = 3
	DefaultDatabaseStorageGiB int32 = 10
	MaxDatabaseStorageGiB     int32 = 500
)

// WithDefaults fills every unset field. Out-of-range values are left for
// Validate to refuse rather than silently clamped. control-plane clamped
// them, which meant a declaration of 50 instances deployed as 3 with no
// error. A declaration the platform cannot honour should fail, not
// quietly shrink.
func (s ManagedDatabaseSpec) WithDefaults() ManagedDatabaseSpec {
	if s.Engine == "" {
		s.Engine = EnginePostgres
	}
	if s.Instances == 0 {
		s.Instances = DefaultDatabaseInstances
	}
	if s.StorageGiB == 0 {
		s.StorageGiB = DefaultDatabaseStorageGiB
	}
	if s.DeletionPolicy == "" {
		s.DeletionPolicy = DeletionPolicyRetain
	}
	return s
}

// ManagedDatabaseStatus is the observed state of a ManagedDatabase.
type ManagedDatabaseStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ClusterName, DatabaseName and RoleName are the identifiers actually
	// created. They are RECORDED rather than re-derived, so that a later
	// change to the naming rule cannot re-point a name that already holds
	// data.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`
	// +optional
	DatabaseName string `json:"databaseName,omitempty"`
	// +optional
	RoleName string `json:"roleName,omitempty"`

	// SecretName is the credential Secret CNPG published, in the Cluster's
	// namespace.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// EngineVersion is the Postgres version CNPG reports running. It is
	// read back rather than declared, because the version is a platform
	// decision.
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`

	// ReadyAt is nil until the Cluster has been healthy, with a published
	// credential, at least once.
	// +optional
	ReadyAt *metav1.Time `json:"readyAt,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// Backup is what the executor observed about this database's backups,
	// nil when the environment configures no object store.
	//
	// NIL AND "UNHEALTHY" ARE DIFFERENT ANSWERS, which is why this is a
	// pointer rather than a value. A database nobody is backing up by
	// design must not report the same state as one whose archiving has
	// broken.
	// +optional
	Backup *DatabaseBackupStatus `json:"backup,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DatabaseBackupStatus answers one question for whoever owns the data: if
// this database broke right now, what would come back?
//
// THREE INDEPENDENT FACTS, NOT A BOOLEAN. A single "backups: ok" hides all
// three of the ways this goes wrong:
//
//   - A recent base backup with dead WAL archiving restores, but only to
//     whenever that backup was taken.
//   - Healthy archiving with no base backup restores to NOTHING. WAL is
//     replayed on top of a base backup; an archive alone is not a backup.
//   - Both healthy, while the retention floor is newer than the incident
//     being investigated, means the moment you want is already gone.
type DatabaseBackupStatus struct {
	// LastSuccessfulAt is when the last base backup completed. Nil means
	// none ever has.
	// +optional
	LastSuccessfulAt *metav1.Time `json:"lastSuccessfulAt,omitempty"`

	// WALArchivingHealthy is whether WAL is currently reaching the object
	// store. THIS IS THE SILENT FAILURE: a database whose archiving has
	// stopped keeps serving traffic normally, and every minute it does is a
	// minute that cannot be recovered to.
	// +optional
	WALArchivingHealthy bool `json:"walArchivingHealthy,omitempty"`

	// EarliestRestorableAt is the oldest point recovery can reach — the
	// floor set by the retention policy. Nil until a first backup exists.
	// +optional
	EarliestRestorableAt *metav1.Time `json:"earliestRestorableAt,omitempty"`

	// LastError is the last archiving or backup error, cleared on success.
	// It is surfaced verbatim: someone who can see "no bucket permission"
	// fixes it, while someone who sees only a false boolean files a ticket.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// ManagedDatabase is a dedicated Postgres.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mdb,categories=forge
// +kubebuilder:printcolumn:name="Engine",type=string,JSONPath=`.spec.engine`
// +kubebuilder:printcolumn:name="Instances",type=integer,JSONPath=`.spec.instances`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ManagedDatabase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedDatabaseSpec   `json:"spec,omitempty"`
	Status ManagedDatabaseStatus `json:"status,omitempty"`
}

// ManagedDatabaseList contains a list of ManagedDatabase.
//
// +kubebuilder:object:root=true
type ManagedDatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedDatabase `json:"items"`
}
