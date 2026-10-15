package scaffold

import (
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

var (
	semverRe = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	// pseudoRe matches the timestamp-commit suffix of a Go pseudo-version.
	pseudoRe = regexp.MustCompile(`(^|[-.])(0\.)?\d{14}-[0-9a-f]{12}$`)
)

var errDevelBuild = errors.New("cannot tell which vogel release this binary belongs to; " +
	"run it as `go run github.com/kafeiih/vogel/cmd/create-vogel-app@vX.Y.Z <name>`, " +
	"pass --version vX.Y.Z, or pass --from <path-to-vogel-checkout>")

// ValidateVersion checks that v is a release tag vMAJOR.MINOR.PATCH[-pre]
// (no build metadata, no pseudo-version).
func ValidateVersion(v string) error {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return fmt.Errorf("invalid version %q: want vMAJOR.MINOR.PATCH[-pre]", v)
	}
	if m[4] != "" && pseudoRe.MatchString(m[4]) {
		return fmt.Errorf("invalid version %q: pseudo-versions have no matching tag", v)
	}
	return nil
}

// BuildVersion returns the module version this binary was built from, as
// reported by read (normally debug.ReadBuildInfo). Development builds,
// pseudo-versions and dirty builds have no matching tag and fail with a
// message pointing at --version or --from.
func BuildVersion(read func() (*debug.BuildInfo, bool)) (string, error) {
	info, ok := read()
	if !ok || info == nil {
		return "", errDevelBuild
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" || ValidateVersion(v) != nil {
		return "", fmt.Errorf("build version %q: %w", v, errDevelBuild)
	}
	return v, nil
}

// CompareVersions compares two vMAJOR.MINOR.PATCH[-pre] versions using
// semantic version precedence and returns -1, 0 or 1.
func CompareVersions(a, b string) (int, error) {
	ma, mb := semverRe.FindStringSubmatch(a), semverRe.FindStringSubmatch(b)
	if ma == nil {
		return 0, fmt.Errorf("invalid version %q", a)
	}
	if mb == nil {
		return 0, fmt.Errorf("invalid version %q", b)
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumeric(ma[i], mb[i]); c != 0 {
			return c, nil
		}
	}
	return comparePrerelease(ma[4], mb[4]), nil
}

// CheckMinVersion fails when version is older than minimum. An empty minimum
// accepts any version.
func CheckMinVersion(version, minimum string) error {
	if minimum == "" {
		return nil
	}
	c, err := CompareVersions(version, minimum)
	if err != nil {
		return err
	}
	if c < 0 {
		return fmt.Errorf("vogel %s is older than the template's minVogelVersion %s; use %s or newer", version, minimum, minimum)
	}
	return nil
}

// compareNumeric compares decimal strings without leading zeros.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, errA := strconv.ParseUint(pa[i], 10, 64)
		nb, errB := strconv.ParseUint(pb[i], 10, 64)
		switch {
		case errA == nil && errB == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case errA == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case errB == nil:
			return 1
		default:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}
