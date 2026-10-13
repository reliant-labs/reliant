package services

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/stretchr/testify/require"
)

func TestPkgmgrPackageTypeFromProto_MapsKnownValues(t *testing.T) {
	pkgType, ok := pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_NPM)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeNPM, pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeMakefile, pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_TASKFILE)
	require.True(t, ok)
	require.Equal(t, pkgmgr.PackageTypeTaskfile, pkgType)
}

func TestPkgmgrPackageTypeFromProto_UnknownDefaultsToFalse(t *testing.T) {
	pkgType, ok := pkgmgrPackageTypeFromProto(reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED)
	require.False(t, ok)
	require.Equal(t, pkgmgr.PackageType(""), pkgType)

	pkgType, ok = pkgmgrPackageTypeFromProto(reliantv1.PackageType(99))
	require.False(t, ok)
	require.Equal(t, pkgmgr.PackageType(""), pkgType)
}
