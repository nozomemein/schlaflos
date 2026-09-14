package config

import (
	"os"
	"regexp"
	"testing"
)

var tomlFence = regexp.MustCompile("(?s)```toml\n(.*?)```")

// TestLLMsTxtExamplesAreValid keeps every TOML example in llms.txt parseable,
// so an assistant that copies one produces a configuration `config check`
// accepts.
func TestLLMsTxtExamplesAreValid(t *testing.T) {
	data, err := os.ReadFile("../../llms.txt")
	if err != nil {
		t.Fatal(err)
	}
	matches := tomlFence.FindAllStringSubmatch(string(data), -1)
	if len(matches) < 4 {
		t.Fatalf("expected several TOML examples in llms.txt, found %d", len(matches))
	}
	for i, m := range matches {
		if _, err := Parse([]byte(m[1])); err != nil {
			t.Errorf("llms.txt example %d is invalid: %v\n%s", i+1, err, m[1])
		}
	}
}
