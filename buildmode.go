//go:build !bindings && !dev

package main

// installSteps is false only in the throwaway binary "wails build" compiles with -tags bindings and runs on the
// build machine to generate its JavaScript bindings, and in "wails dev" (-tags dev). Those must never install,
// update or uninstall anything: they would install themselves into the developer's own profile.
const installSteps = true
