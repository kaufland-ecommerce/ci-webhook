package hook_manager

import (
	"os"
	"reflect"
	"testing"

	"github.com/kaufland-ecommerce/ci-webhook/internal/hook"
)

var hooksLoadFromFileTests = []struct {
	path       string
	asTemplate bool
	ok         bool
}{
	{"../../hooks.json.example", false, true},
	{"../../hooks.yaml.example", false, true},
	{"../../hooks.json.tmpl.example", true, true},
	{"../../hooks.yaml.tmpl.example", true, true},
	{"", false, true},
	// failures
	{"missing.json", false, false},
}

func TestHooksLoadFromFile(t *testing.T) {
	secret := `foo"123`
	_ = os.Setenv("XXXTEST_SECRET", secret)

	for _, tt := range hooksLoadFromFileTests {
		t.Run(tt.path, func(t *testing.T) {
			h := &Hooks{}
			err := h.LoadFromFile(tt.path, tt.asTemplate)
			if (err == nil) != tt.ok {
				t.Errorf(err.Error())
			}
		})
	}
}

func TestHooksTemplateLoadFromFile(t *testing.T) {
	secret := `foo"123`
	_ = os.Setenv("XXXTEST_SECRET", secret)

	for _, tt := range hooksLoadFromFileTests {
		if !tt.asTemplate {
			continue
		}
		t.Run(tt.path, func(t *testing.T) {
			h := &Hooks{}
			err := h.LoadFromFile(tt.path, tt.asTemplate)
			if (err == nil) != tt.ok {
				t.Errorf(err.Error())
				return
			}

			s := (*h.Match("webhook").TriggerRule.And)[0].Match.Secret
			if s != secret {
				t.Errorf("Expected secret of %q, got %q", secret, s)
			}
		})
	}
}

var hooksMatchTests = []struct {
	id    string
	hooks Hooks
	value *hook.Hook
}{
	{"a", Hooks{hook.Hook{ID: "a"}}, &hook.Hook{ID: "a"}},
	{"X", Hooks{hook.Hook{ID: "a"}}, new(hook.Hook)},
}

func TestHooksMatch(t *testing.T) {
	for _, tt := range hooksMatchTests {
		value := tt.hooks.Match(tt.id)
		if reflect.DeepEqual(reflect.ValueOf(value), reflect.ValueOf(tt.value)) {
			t.Errorf("failed to match %q:\nexpected %#v,\ngot %#v", tt.id, tt.value, value)
		}
	}
}
