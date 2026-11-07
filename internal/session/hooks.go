package session

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bogdanlinitskiy/claude-inbox/internal/hook"
	"github.com/bogdanlinitskiy/claude-inbox/internal/settings"
)

const settingsMode = 0o600

// writeHookSettings writes the settings file passed to every session with claude --settings. It is rewritten on
// every start, so it always points at the current binary.
func writeHookSettings(path string, command []string) error {
	quoted := make([]string, len(command))
	for i, arg := range command {
		quoted[i] = settings.Quote(arg)
	}
	entry := []settings.Matcher{settings.Entry(strings.Join(quoted, " "))}
	hooks := make(map[string][]settings.Matcher, len(hook.Events))
	for _, event := range hook.Events {
		hooks[event] = entry
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hook settings: %w", err)
	}
	if err := os.WriteFile(path, data, settingsMode); err != nil {
		return fmt.Errorf("write hook settings: %w", err)
	}
	return nil
}
