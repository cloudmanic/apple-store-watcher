//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"
)

// Default settings used when config.json leaves a value out. Apple blocks
// lookups made once a minute, so checks run every 5 minutes.
const (
	defaultCheckIntervalSeconds = 300
	defaultTextIntervalSeconds  = 180
	defaultMaxTextsPerTrigger   = 30
	defaultLogFile              = "watcher.log"
)

// storeNumberPattern matches Apple's retail store IDs, like R077.
var storeNumberPattern = regexp.MustCompile(`^R\d+$`)

// phoneNumberPattern matches the +15035551234 form Twilio needs.
var phoneNumberPattern = regexp.MustCompile(`^\+\d{10,15}$`)

// Config is what to watch and how often to text. It lives in config.json,
// which stays out of git because it holds phone numbers.
type Config struct {
	CheckIntervalSeconds int       `json:"check_interval_seconds"`
	TextIntervalSeconds  int       `json:"text_interval_seconds"`
	MaxTextsPerTrigger   int       `json:"max_texts_per_trigger"`
	LogFile              string    `json:"log_file"`
	TextTo               []string  `json:"text_to"`
	TextLink             string    `json:"text_link"`
	SearchLocation       string    `json:"search_location"`
	Stores               []Store   `json:"stores"`
	Products             []Product `json:"products"`
}

// Store is one Apple Store to watch. Name is only for logs and texts.
type Store struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

// Product is one Apple part number to watch. Name is only for logs and texts.
type Product struct {
	Part string `json:"part"`
	Name string `json:"name"`
}

// LoadConfig reads and checks the config file. Unknown keys are an error so a
// typo cannot silently turn a setting off.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w (copy config.example.json to start)", path, err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return &cfg, nil
}

// applyDefaults fills in any setting left at zero so a short config still works.
func (c *Config) applyDefaults() {
	if c.CheckIntervalSeconds == 0 {
		c.CheckIntervalSeconds = defaultCheckIntervalSeconds
	}

	if c.TextIntervalSeconds == 0 {
		c.TextIntervalSeconds = defaultTextIntervalSeconds
	}

	if c.MaxTextsPerTrigger == 0 {
		c.MaxTextsPerTrigger = defaultMaxTextsPerTrigger
	}

	if strings.TrimSpace(c.LogFile) == "" {
		c.LogFile = defaultLogFile
	}
}

// Validate returns every problem in the config at once, so one run shows the
// whole list instead of one fix per restart.
func (c *Config) Validate() error {
	var problems []error

	if c.CheckIntervalSeconds < 0 {
		problems = append(problems, errors.New("check_interval_seconds must be positive"))
	}

	if c.TextIntervalSeconds < 0 {
		problems = append(problems, errors.New("text_interval_seconds must be positive"))
	}

	if c.MaxTextsPerTrigger < 0 {
		problems = append(problems, errors.New("max_texts_per_trigger must be positive"))
	}

	if len(c.TextTo) == 0 {
		problems = append(problems, errors.New("text_to needs at least one phone number"))
	}

	for _, n := range c.TextTo {
		if !phoneNumberPattern.MatchString(n) {
			problems = append(problems, fmt.Errorf("text_to %q must look like +15035551234", n))
		}
	}

	if strings.TrimSpace(c.SearchLocation) == "" {
		problems = append(problems, errors.New("search_location needs a zip code near the stores, like 97201"))
	}

	if len(c.Stores) == 0 {
		problems = append(problems, errors.New("stores needs at least one store"))
	}

	seenStores := map[string]bool{}
	for _, s := range c.Stores {
		if !storeNumberPattern.MatchString(s.Number) {
			problems = append(problems, fmt.Errorf("store number %q must look like R077", s.Number))
		}

		if seenStores[s.Number] {
			problems = append(problems, fmt.Errorf("store %s is listed twice", s.Number))
		}

		seenStores[s.Number] = true
	}

	if len(c.Products) == 0 {
		problems = append(problems, errors.New("products needs at least one part number"))
	}

	seenParts := map[string]bool{}
	for _, p := range c.Products {
		if strings.TrimSpace(p.Part) == "" {
			problems = append(problems, errors.New("every product needs a part number"))
			continue
		}

		if seenParts[p.Part] {
			problems = append(problems, fmt.Errorf("part %s is listed twice", p.Part))
		}

		seenParts[p.Part] = true
	}

	return errors.Join(problems...)
}

// CheckInterval is how long to wait between stock checks.
func (c *Config) CheckInterval() time.Duration {
	return time.Duration(c.CheckIntervalSeconds) * time.Second
}

// TextInterval is the shortest gap allowed between two texts.
func (c *Config) TextInterval() time.Duration {
	return time.Duration(c.TextIntervalSeconds) * time.Second
}

// PartNumbers lists the part numbers in config order, which is the order
// they are sent to Apple and logged.
func (c *Config) PartNumbers() []string {
	parts := make([]string, 0, len(c.Products))

	for _, p := range c.Products {
		parts = append(parts, p.Part)
	}

	return parts
}

// StoreName picks the friendliest name for a store: the config name, then
// Apple's name, then the bare store number.
func (c *Config) StoreName(number, appleName string) string {
	for _, s := range c.Stores {
		if s.Number == number && s.Name != "" {
			return s.Name
		}
	}

	if appleName != "" {
		return appleName
	}

	return number
}

// ProductName picks the friendliest name for a part: the config name, then
// Apple's name, then the bare part number.
func (c *Config) ProductName(part, appleName string) string {
	for _, p := range c.Products {
		if p.Part == part && p.Name != "" {
			return p.Name
		}
	}

	if appleName != "" {
		return appleName
	}

	return part
}

// LogSummary writes the full config to the log at startup, so the log file
// always shows what a run was watching.
func (c *Config) LogSummary(log *slog.Logger) {
	log.Info("settings",
		"check_every", c.CheckInterval(),
		"text_every", c.TextInterval(),
		"max_texts_per_trigger", c.MaxTextsPerTrigger,
		"text_to", strings.Join(c.TextTo, ","),
		"text_link", c.TextLink,
		"search_location", c.SearchLocation,
		"log_file", c.LogFile,
	)

	for _, s := range c.Stores {
		log.Info("watching store", "store", s.Number, "store_name", c.StoreName(s.Number, ""))
	}

	for _, p := range c.Products {
		log.Info("watching product", "part", p.Part, "product", c.ProductName(p.Part, ""))
	}
}
