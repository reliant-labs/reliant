// Package ciguard holds forge's own CI wiring to the invariants that keep a
// sharded, path-filtered pipeline from silently testing less than it claims.
//
// The pull-request test tier is a matrix of shards (.github/workflows/ci.yml's
// `test-short`), each naming its slice as "I of N". A shard deleted from the
// matrix takes its packages with it while every remaining check stays green,
// which is the failure mode sharding introduces. The guards are the tests in
// ciguard_test.go; this file exists so the directory is a buildable package.
// There is no runtime code here on purpose.
package ciguard
