package scaffold

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	nameRe        = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	ownerRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	moduleHostRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.[a-z0-9.-]*[a-z0-9]$`)
	moduleElemRe  = regexp.MustCompile(`^[A-Za-z0-9_~.-]+$`)
	componentRe   = regexp.MustCompile(`^[a-z]+$`)
	maxNameLength = 50
)

// ValidateName checks the project name: ^[a-z][a-z0-9-]*$, at most 50 chars.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) || len(name) > maxNameLength {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and dashes, starting with a letter, at most %d characters", name, maxNameLength)
	}
	return nil
}

// ValidateOwner checks a GitHub user or organization name.
func ValidateOwner(owner string) error {
	if !ownerRe.MatchString(owner) {
		return fmt.Errorf("invalid owner %q: want a GitHub user or organization name", owner)
	}
	return nil
}

// ValidateModulePath checks that module looks like a Go module path: a
// lowercase domain-like first element followed by at least one path element.
func ValidateModulePath(module string) error {
	elems := strings.Split(module, "/")
	if len(elems) < 2 || !moduleHostRe.MatchString(elems[0]) {
		return fmt.Errorf("invalid module path %q: want something like github.com/<owner>/<name>", module)
	}
	for _, e := range elems[1:] {
		if !moduleElemRe.MatchString(e) || strings.HasPrefix(e, ".") || strings.HasSuffix(e, ".") {
			return fmt.Errorf("invalid module path %q: bad element %q", module, e)
		}
	}
	return nil
}

// TemplateValues are the fields available to replacement templates.
type TemplateValues struct {
	Name      string
	DBName    string
	Owner     string
	ImageBase string
	Module    string
}

// Derive computes the template values for a project.
func Derive(name, owner, module string) TemplateValues {
	return TemplateValues{
		Name:      name,
		DBName:    strings.ReplaceAll(name, "-", "_"),
		Owner:     owner,
		ImageBase: "ghcr.io/" + strings.ToLower(owner) + "/" + name,
		Module:    module,
	}
}

func (v TemplateValues) asMap() map[string]string {
	return map[string]string{
		"Name":      v.Name,
		"DBName":    v.DBName,
		"Owner":     v.Owner,
		"ImageBase": v.ImageBase,
		"Module":    v.Module,
	}
}
