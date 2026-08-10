package config

import "testing"

func TestExpandEnv(t *testing.T) {
	t.Setenv("EXPAND_ENV_TEST_VAR", "hello")
	t.Setenv("EXPAND_ENV_TEST_EMPTY", "")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"set var", "value: ${EXPAND_ENV_TEST_VAR}", "value: hello"},
		{"unset var no default", "value: ${THIS_VAR_DOES_NOT_EXIST}", "value: "},
		{"unset var with default", "value: ${THIS_VAR_DOES_NOT_EXIST:-fallback}", "value: fallback"},
		{"set var with default still uses env value", "value: ${EXPAND_ENV_TEST_VAR:-fallback}", "value: hello"},
		{"set-but-empty var uses empty, not default", "value: ${EXPAND_ENV_TEST_EMPTY:-fallback}", "value: "},
		{"multiple vars", "${EXPAND_ENV_TEST_VAR}-${EXPAND_ENV_TEST_VAR}", "hello-hello"},
		{"no vars", "plain text, no substitution", "plain text, no substitution"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(expandEnv([]byte(tc.input)))
			if got != tc.want {
				t.Errorf("expandEnv(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
