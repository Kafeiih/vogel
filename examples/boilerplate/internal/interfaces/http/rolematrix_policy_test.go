package http

import (
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// cerbosPolicyFile declares only the policy features matrixPolicy can model.
// Decoding is strict, so a policy that uses anything else (conditions on
// resource attributes, derived roles, scopes, schemas) fails this test
// instead of being compared as if its roles alone decided access.
type cerbosPolicyFile struct {
	APIVersion     string `yaml:"apiVersion"`
	ResourcePolicy *struct {
		Version  string `yaml:"version"`
		Resource string `yaml:"resource"`
		Rules    []struct {
			Name    string   `yaml:"name"`
			Actions []string `yaml:"actions"`
			Effect  string   `yaml:"effect"`
			Roles   []string `yaml:"roles"`
		} `yaml:"rules"`
	} `yaml:"resourcePolicy"`
}

// TestMatrixPolicyMatchesCerbosPolicies fails when matrixPolicy, the PDP
// stand-in the role matrix runs against, no longer grants exactly what the
// bundled Cerbos policies grant. Without it a policy change would leave
// TestNewRouter_RoleMatrix green while the deployed PDP decides otherwise.
func TestMatrixPolicyMatchesCerbosPolicies(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "cerbos", "policies", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no policies found in cerbos/policies")
	}

	fromPolicies := map[string]map[string][]string{}
	for _, file := range files {
		policy := readCerbosPolicy(t, file)
		rp := policy.ResourcePolicy
		if rp == nil {
			t.Errorf("%s: not a resource policy, which matrixPolicy cannot model", file)
			continue
		}
		if rp.Version != "default" {
			t.Errorf("%s: version %q, but RequirePermission asks for the default version", file, rp.Version)
		}
		for i, rule := range rp.Rules {
			if rule.Effect != "EFFECT_ALLOW" {
				t.Errorf("%s: rule %d has effect %q; matrixPolicy only models EFFECT_ALLOW", file, i, rule.Effect)
				continue
			}
			for _, action := range rule.Actions {
				for _, role := range rule.Roles {
					if action == "*" || role == "*" {
						t.Errorf("%s: rule %d uses a wildcard, which matrixPolicy cannot model", file, i)
						continue
					}
					if fromPolicies[rp.Resource] == nil {
						fromPolicies[rp.Resource] = map[string][]string{}
					}
					fromPolicies[rp.Resource][action] = append(fromPolicies[rp.Resource][action], role)
				}
			}
		}
	}

	if got, want := normalizeMatrix(matrixPolicy), normalizeMatrix(fromPolicies); !reflect.DeepEqual(got, want) {
		t.Errorf("matrixPolicy = %v, but cerbos/policies grant %v; update matrixPolicy and the role matrix rows", got, want)
	}
}

func readCerbosPolicy(t *testing.T, file string) cerbosPolicyFile {
	t.Helper()
	f, err := os.Open(file) //nolint:gosec // file comes from a glob over the repository's cerbos/policies
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var policy cerbosPolicyFile
	if err := dec.Decode(&policy); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: holds more than one YAML document (%v)", file, err)
	}
	return policy
}

// normalizeMatrix sorts and deduplicates every role list so that rule order
// and repeated grants do not count as drift.
func normalizeMatrix(m map[string]map[string][]string) map[string]map[string][]string {
	out := make(map[string]map[string][]string, len(m))
	for kind, actions := range m {
		out[kind] = make(map[string][]string, len(actions))
		for _, action := range slices.Sorted(maps.Keys(actions)) {
			out[kind][action] = slices.Compact(slices.Sorted(slices.Values(actions[action])))
		}
	}
	return out
}
