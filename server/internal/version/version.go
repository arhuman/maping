// Package version carries the build-time identity of the maping-server binary.
// The values are injected by the linker from the Makefile and the Dockerfile;
// a build that does not pass the flags (plain `go build`, `go test`) keeps the
// placeholders rather than failing, so tests and local runs need no stamping.
package version

// Version is the released semver of this binary, injected at link time with
// -X github.com/arhuman/maping/server/internal/version.Version=vX.Y.Z. It is
// "dev" for any build that did not pass the flag; callers must not parse it as
// semver without checking for that placeholder first.
var Version = "dev"

// Commit is the git revision the binary was built from, injected at link time
// alongside Version. It is "none" for an unstamped build.
var Commit = "none"

// String renders the single line printed by `maping-server --version`. The
// shape is "maping-server <version> (<commit>)" and is meant for humans and
// support tickets, not for machine parsing.
func String() string {
	return "maping-server " + Version + " (" + Commit + ")"
}
