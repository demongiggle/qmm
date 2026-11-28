package server

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// encodeSettings keeps the same section and option order as the settings page.
// Encode each assignment with the TOML encoder so it handles quoting and escaping.
func encodeSettings(w io.Writer, st *settingsState) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	for i, section := range settingsSections {
		if i > 0 {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "[%s]\n", toml.Key{section.TOMLTable})
		for _, def := range section.Options {
			buf.WriteString("  ")
			if err := enc.Encode(map[string]string{def.TOMLKey: st.text(def)}); err != nil {
				return err
			}
		}
	}
	_, err := buf.WriteTo(w)
	return err
}

func decodeSettings(path string) (settingsState, error) {
	st := defaultSettings()
	var tables map[string]any
	md, err := toml.DecodeFile(path, &tables)
	if err != nil {
		return st, fmt.Errorf("could not parse config: %w", err)
	}
	// The pre-theme format used these three colors as defaults. A file with
	// an explicit theme (even an empty string) keeps its explicit colors.
	preTheme := !md.IsDefined("appearance", "theme")
	for _, section := range settingsSections {
		for name, raw := range tables {
			// The original struct decoder accepted case-insensitive names.
			if !strings.EqualFold(name, section.TOMLTable) {
				continue
			}
			fields, ok := raw.(map[string]any)
			if !ok {
				return st, fmt.Errorf("could not parse config: %s must be a table", name)
			}
			if err := decodeSettingsSection(fields, section, &st, preTheme); err != nil {
				return st, err
			}
		}
	}
	return st, nil
}

func decodeSettingsSection(fields map[string]any, section settingsSection, st *settingsState, preTheme bool) error {
	for _, def := range section.Options {
		for name, field := range fields {
			if !strings.EqualFold(name, def.TOMLKey) {
				continue
			}
			value, ok := field.(string)
			if !ok {
				return fmt.Errorf("could not parse config: %s.%s must be a string", section.TOMLTable, name)
			}
			if value == "" || preTheme && isLegacyThemeDefault(def.ID, value) {
				continue
			}
			idx := slices.Index(def.Values, value)
			if idx < 0 {
				return fmt.Errorf("invalid value %q for %q", value, def.ID)
			}
			st.values[def.ID] = idx
		}
	}
	return nil
}

func isLegacyThemeDefault(id settingID, value string) bool {
	switch id {
	case settingStatusBarColor:
		return value == statusBarNames[0]
	case settingBorderColor:
		return value == accentColorNames[0]
	case settingCopyStatusColor:
		return value == statusBarNames[1]
	}
	return false
}
