package hook_manager

import (
	"bytes"
	"fmt"
	"os"
	"text/template"

	"github.com/ghodss/yaml"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
)

// Hooks is an array of Hook objects
type Hooks []hook.Hook

// LoadFromFile attempts to load hooks from the specified file, which
// can be either JSON or YAML.  The asTemplate parameter causes the file
// contents to be parsed as a Go text/template prior to unmarshalling.
func (h *Hooks) LoadFromFile(path string, asTemplate bool) error {
	if path == "" {
		return nil
	}

	// parse hook file for hooks
	file, e := os.ReadFile(path)
	if e != nil {
		return fmt.Errorf("error reading hooks file: [%s]: %w", path, e)
	}

	if asTemplate {
		funcMap := template.FuncMap{"getenv": getenv}

		tmpl, err := template.New("hooks").Funcs(funcMap).Parse(string(file))
		if err != nil {
			return fmt.Errorf("error parsing hooks file: [%s]: %w", path, err)
		}

		var buf bytes.Buffer
		err = tmpl.Execute(&buf, nil)
		if err != nil {
			return fmt.Errorf("executing template on file [%s]: %w", path, err)
		}

		file = buf.Bytes()
	}

	return yaml.Unmarshal(file, h)
}

// Append appends hooks unless the new hooks contain a hook with an ID that already exists
func (h *Hooks) Append(other *Hooks) error {
	for _, hook := range *other {
		if h.Match(hook.ID) != nil {
			return fmt.Errorf("hook with ID %s is already defined", hook.ID)
		}

		*h = append(*h, hook)
	}

	return nil
}

// Match iterates through Hooks and returns first one that matches the given ID,
// if no hook matches the given ID, nil is returned
func (h *Hooks) Match(id string) *hook.Hook {
	for i := range *h {
		if (*h)[i].ID == id {
			return &(*h)[i]
		}
	}

	return nil
}

// getenv provides a template function to retrieve OS environment variables.
func getenv(s string) string {
	return os.Getenv(s)
}
