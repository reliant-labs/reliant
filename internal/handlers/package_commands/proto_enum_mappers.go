// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// packageTypeFromString maps a discovered command's package type onto the
// proto enum.
func packageTypeFromString(s string) reliantv1.PackageType {
	switch s {
	case "npm":
		return reliantv1.PackageType_PACKAGE_TYPE_NPM
	case "makefile":
		return reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE
	case "taskfile":
		return reliantv1.PackageType_PACKAGE_TYPE_TASKFILE
	default:
		return reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED
	}
}

// pkgmgrPackageTypeFromProto maps a requested package type back onto pkgmgr's;
// false for one it cannot run.
func pkgmgrPackageTypeFromProto(pt reliantv1.PackageType) (pkgmgr.PackageType, bool) {
	switch pt {
	case reliantv1.PackageType_PACKAGE_TYPE_NPM:
		return pkgmgr.PackageTypeNPM, true
	case reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE:
		return pkgmgr.PackageTypeMakefile, true
	case reliantv1.PackageType_PACKAGE_TYPE_TASKFILE:
		return pkgmgr.PackageTypeTaskfile, true
	default:
		return "", false
	}
}
