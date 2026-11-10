// Package core holds the shared vocabulary of the tester: runs, results,
// statuses, the manifest types, and the interfaces (the "ports") through which
// the rest of the program touches the outside world.
//
// The dependency rule: this package imports only the Go standard library and
// never another package of this module. Everything else depends on core, so
// core can be read, tested and changed without any adapter, database, network
// or project in sight. imports_test.go enforces the rule.
//
// The tester knows no particular project. A project describes itself through
// its manifest (see manifest.go); nothing in this package names a project, a
// test tool or a service.
package core
