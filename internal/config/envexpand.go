package config

import (
	"os"
	"regexp"
)

// envVarPattern matches ${VAR} and ${VAR:-default}, mirroring familiar
// shell/docker-compose parameter expansion syntax.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandEnv substitutes ${VAR} / ${VAR:-default} references in raw
// config bytes with values from the process environment, so secrets
// (gateway API keys, Redis password) never need to be committed in
// plaintext (hardens NFR5). An unset variable with no default expands
// to an empty string.
func expandEnv(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		sub := envVarPattern.FindSubmatch(match)
		name := string(sub[1])
		def := string(sub[3])
		if v, ok := os.LookupEnv(name); ok {
			return []byte(v)
		}
		return []byte(def)
	})
}
