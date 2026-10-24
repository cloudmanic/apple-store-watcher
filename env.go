//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// LoadEnvFile copies KEY=VALUE lines from path into the environment and
// returns how many it set. A value already set in the shell wins, so a
// one-off override needs no file edit. A missing file is fine; the caller
// checks for the values it needs.
func LoadEnvFile(path string) (int, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer f.Close()

	loaded := 0
	lineNo := 0
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		lineNo++

		key, value, ok, err := parseEnvLine(scanner.Text())
		if err != nil {
			return loaded, fmt.Errorf("env file %s line %d: %w", path, lineNo, err)
		}

		if !ok {
			continue
		}

		if existing, set := os.LookupEnv(key); set && existing != "" {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			return loaded, fmt.Errorf("env file %s line %d: %w", path, lineNo, err)
		}

		loaded++
	}

	if err := scanner.Err(); err != nil {
		return loaded, fmt.Errorf("read env file %s: %w", path, err)
	}

	return loaded, nil
}

// parseEnvLine turns one .env line into a key and value. ok is false for
// blank lines and comments. It accepts the common forms people paste in:
// an "export " prefix, quoted values, and a trailing " # comment".
func parseEnvLine(line string) (key, value string, ok bool, err error) {
	line = strings.TrimSpace(line)

	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}

	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false, errors.New("expected KEY=VALUE")
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false, errors.New("missing key before =")
	}

	value = strings.TrimSpace(value)

	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return key, value[1 : len(value)-1], true, nil
	}

	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}

	return key, value, true, nil
}
