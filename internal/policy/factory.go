package policy

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/eyelock/ynf"
	"gopkg.in/yaml.v3"
)

// FactoryFile is a configuration repository's factory file, in its factory folder (ADR-006).
const FactoryFile = "factory.yaml"

// Factory is a configuration repository's factory.yaml: enrolment, and the tracker and forge
// instances, which are declared only here.
type Factory struct {
	Version  int                       `yaml:"version" json:"version"`
	Repos    []string                  `yaml:"repos" json:"repos"`
	Trackers map[string]map[string]any `yaml:"trackers" json:"trackers,omitempty"`
	Forges   map[string]map[string]any `yaml:"forges" json:"forges,omitempty"`
}

// LoadFactory validates and reads a factory.yaml.
func LoadFactory(doc []byte) (*Factory, error) {
	if err := ValidateYAML(ynf.FactorySchema, "https://eyelock.github.io/ynf/schema/factory.schema.json", doc); err != nil {
		return nil, fmt.Errorf("factory.yaml does not match the schema:\n%w", err)
	}
	var f Factory
	if err := yaml.Unmarshal(doc, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// MergeLanes lays a target repository's lanes over a configuration repository's, key by key: the
// target repository wins (ADR-006). Maps merge; anything else in the repository replaces the
// configuration's, lists included, so a repository can narrow a list as well as extend it. Either
// may be empty. The result is validated as a whole when it is loaded, so a repository may hold
// only overrides, such as turning a lane off with enabled: false.
func MergeLanes(config, repo []byte) ([]byte, error) {
	var c, r map[string]any
	if err := yaml.Unmarshal(config, &c); err != nil {
		return nil, fmt.Errorf("the configuration repository's lanes: %w", err)
	}
	if err := yaml.Unmarshal(repo, &r); err != nil {
		return nil, fmt.Errorf("the repository's lanes: %w", err)
	}
	return yaml.Marshal(merge(c, r))
}

func merge(base, over map[string]any) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range over {
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = merge(bm, om)
		} else {
			out[k] = v
		}
	}
	return out
}

// LaneSources says where each of a lane's values came from: every leaf path in the merged lane
// (dotted, such as run.ynh.focus) maps to "repo" when the repository's lanes set it and "config"
// when only the configuration repository's do.
func LaneSources(config, repo []byte, lane string) (map[string]string, error) {
	layer := func(doc []byte) (map[string]any, error) {
		var d struct {
			Lanes map[string]map[string]any `yaml:"lanes"`
		}
		if err := yaml.Unmarshal(doc, &d); err != nil {
			return nil, err
		}
		return d.Lanes[lane], nil
	}
	c, err := layer(config)
	if err != nil {
		return nil, err
	}
	r, err := layer(repo)
	if err != nil {
		return nil, err
	}
	fromRepo := map[string]bool{}
	leaves(r, "", func(p string) { fromRepo[p] = true })
	out := map[string]string{}
	leaves(merge(c, r), "", func(p string) {
		if fromRepo[p] {
			out[p] = "repo"
		} else {
			out[p] = "config"
		}
	})
	return out, nil
}

func leaves(m map[string]any, prefix string, f func(string)) {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		p := strings.TrimPrefix(prefix+"."+k, ".")
		if sub, ok := m[k].(map[string]any); ok && len(sub) > 0 {
			leaves(sub, p, f)
			continue
		}
		f(p)
	}
}
