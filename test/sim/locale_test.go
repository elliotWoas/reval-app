package sim

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"taskbot/internal/testkit"
)

var namespaces = []string{"common", "start", "menu", "more", "create", "days", "time", "checkins", "invite", "group",
	"join", "cycle", "plan", "checkin", "evening", "reasons", "report", "status", "members", "leave", "owner",
	"settings", "weekly", "errors"}

// TestLocaleKeys checks that every key used in code exists in locales/fa.yaml,
// including keys built from config values and error codes.
func TestLocaleKeys(t *testing.T) {
	b := testkit.Bundle(t)
	cfg := testkit.Config(t)
	re := regexp.MustCompile(`"((?:` + strings.Join(namespaces, "|") + `)\.[a-z_0-9]+)"`)
	used := map[string]bool{}
	err := filepath.Walk(filepath.Join(testkit.Root(), "internal"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Options.FailReasons {
		used["reasons."+r] = true
	}
	for _, p := range cfg.Options.WorkDayPresets {
		used["days."+p.Key] = true
	}
	for _, p := range cfg.Options.Checkins {
		used["checkins."+p.Key] = true
	}
	for _, f := range []string{"morning", "deadline", "evening", "summary", "weekly", "checkins"} {
		used["time.ask_"+f] = true
	}
	src, _ := os.ReadFile(filepath.Join(testkit.Root(), "internal", "domain", "errors.go"))
	for _, m := range regexp.MustCompile(`E\("([a-z_]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		used["errors."+m[1]] = true
	}
	delete(used, "time.ask_") // prefix used to build keys
	for k := range used {
		if !b.Has(k) {
			t.Errorf("missing locale key %q", k)
		}
	}
	// menu labels must be unique, they are matched by text
	seen := map[string]string{}
	for _, k := range b.Keys() {
		if strings.HasPrefix(k, "menu.") && k != "menu.title" {
			if o, dup := seen[b.T(k)]; dup {
				t.Errorf("menu label of %s duplicates %s", k, o)
			}
			seen[b.T(k)] = k
		}
	}
}

// TestCallbackDataFits makes sure the longest callback data stays under Telegram's 64 bytes.
func TestCallbackDataFits(t *testing.T) {
	planID := strings.Repeat("a", 24) + ":2026-11-18"
	longest := "ev|r|" + planID + "|20|dependency"
	if len(longest) > 64 {
		t.Fatalf("callback data %d bytes", len(longest))
	}
}
