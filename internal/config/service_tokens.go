// @nexus-project: nexus
// @nexus-path: internal/config/service_tokens.go
// LoadServiceTokens reads the service-tokens file and returns a map
// of service name to token. Used by the ServiceAuth middleware.
//
// File format (one entry per line, fields separated by whitespace):
//
//	atlas  <uuid>
//	forge  <uuid>
//
// Lines beginning with # are ignored. Unknown service names are accepted
// and stored — the middleware decides which names are valid callers.
//
// If the file does not exist, an empty map is returned with no error.
// The API server runs in unauthenticated mode and logs a WARNING at startup.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ServiceTokensPath is the default location of the service-tokens file.
const ServiceTokensPath = "~/.nexus/service-tokens"

// LoadServiceTokens reads tokenFilePath and returns a map of
// service name → token string. Returns an empty map if the file
// does not exist (unauthenticated mode). Returns an error only
// on file read or parse failure.
func LoadServiceTokens(tokenFilePath string) (map[string]string, error) {
	path := ExpandHome(tokenFilePath)

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil // unauthenticated mode — caller logs WARNING
	}
	if err != nil {
		return nil, fmt.Errorf("open service-tokens: %w", err)
	}
	defer f.Close()

	tokens := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("service-tokens line %d: expected '<name> <token>', got %q",
				lineNum, line)
		}
		name, token := fields[0], fields[1]
		if name == "" || token == "" {
			return nil, fmt.Errorf("service-tokens line %d: empty name or token", lineNum)
		}
		tokens[name] = token
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read service-tokens: %w", err)
	}
	return tokens, nil
}
