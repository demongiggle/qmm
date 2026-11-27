// Package theme defines the built-in terminal color palettes shared by qmm's
// chrome and pane rendering. The palette catalog mirrors qcode's terminal
// themes; each named palette also materializes a full 256-color ANSI table.
package theme

import (
	"strconv"
	"strings"
)

// Palette is one named theme. The role colors come from qcode's terminal
// themes. ANSI256 is a materialized xterm-style table: entries 0-15 map the
// semantic roles, and 16-255 are the standard xterm cube and grayscale ramp
// tinted toward this palette. The default palette leaves ANSI256 zero and is
// not applied to pane output.
type Palette struct {
	ID, Name, Appearance string
	Background           string
	Text                 string
	Prompt               string
	Markdown             string
	Status               string
	Diff                 string
	Success              string
	Warning              string
	Error                string
	Accent               string
	Muted                string
	Link                 string
	ANSI256              [256]int32
}

var rawPalettes = []Palette{
	{ID: "default", Name: "Default", Appearance: "Auto"},
	{ID: "catppuccin-mocha", Name: "Catppuccin Mocha", Appearance: "Dark", Background: "#1e1e2e", Text: "#cdd6f4", Prompt: "#89b4fa", Markdown: "#cdd6f4", Status: "#cba6f7", Diff: "#f9e2af", Success: "#a6e3a1", Warning: "#f9e2af", Error: "#f38ba8", Accent: "#cba6f7", Muted: "#a6adc8", Link: "#89dceb"},
	{ID: "dracula", Name: "Dracula", Appearance: "Dark", Background: "#282a36", Text: "#f8f8f2", Prompt: "#8be9fd", Markdown: "#f8f8f2", Status: "#bd93f9", Diff: "#f1fa8c", Success: "#50fa7b", Warning: "#f1fa8c", Error: "#ff5555", Accent: "#ff79c6", Muted: "#6272a4", Link: "#8be9fd"},
	{ID: "gruvbox-dark", Name: "Gruvbox Dark", Appearance: "Dark", Background: "#282828", Text: "#ebdbb2", Prompt: "#8ec07c", Markdown: "#ebdbb2", Status: "#d3869b", Diff: "#fabd2f", Success: "#b8bb26", Warning: "#fabd2f", Error: "#fb4934", Accent: "#d3869b", Muted: "#a89984", Link: "#8ec07c"},
	{ID: "solarized-dark", Name: "Solarized Dark", Appearance: "Dark", Background: "#002b36", Text: "#839496", Prompt: "#268bd2", Markdown: "#839496", Status: "#6c71c4", Diff: "#b58900", Success: "#859900", Warning: "#b58900", Error: "#dc322f", Accent: "#d33682", Muted: "#586e75", Link: "#2aa198"},
	{ID: "nord-dark", Name: "Nord Dark", Appearance: "Dark", Background: "#2e3440", Text: "#d8dee9", Prompt: "#88c0d0", Markdown: "#d8dee9", Status: "#81a1c1", Diff: "#ebcb8b", Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Accent: "#b48ead", Muted: "#616e88", Link: "#8fbcbb"},
	{ID: "tokyo-night", Name: "Tokyo Night", Appearance: "Dark", Background: "#1a1b26", Text: "#c0caf5", Prompt: "#7aa2f7", Markdown: "#c0caf5", Status: "#bb9af7", Diff: "#e0af68", Success: "#9ece6a", Warning: "#e0af68", Error: "#f7768e", Accent: "#bb9af7", Muted: "#a9b1d6", Link: "#7dcfff"},
	{ID: "one-dark", Name: "One Dark", Appearance: "Dark", Background: "#282c34", Text: "#abb2bf", Prompt: "#61afef", Markdown: "#abb2bf", Status: "#c678dd", Diff: "#e5c07b", Success: "#98c379", Warning: "#e5c07b", Error: "#e06c75", Accent: "#c678dd", Muted: "#828997", Link: "#56b6c2"},
	{ID: "rose-pine", Name: "Rosé Pine", Appearance: "Dark", Background: "#191724", Text: "#e0def4", Prompt: "#9ccfd8", Markdown: "#e0def4", Status: "#c4a7e7", Diff: "#f6c177", Success: "#95b1ac", Warning: "#f6c177", Error: "#eb6f92", Accent: "#ebbcba", Muted: "#908caa", Link: "#9ccfd8"},
	{ID: "everforest-dark", Name: "Everforest Dark", Appearance: "Dark", Background: "#2d353b", Text: "#d3c6aa", Prompt: "#7fbbb3", Markdown: "#d3c6aa", Status: "#d699b6", Diff: "#dbbc7f", Success: "#a7c080", Warning: "#dbbc7f", Error: "#e67e80", Accent: "#83c092", Muted: "#9da9a0", Link: "#7fbbb3"},
	{ID: "kanagawa-wave", Name: "Kanagawa Wave", Appearance: "Dark", Background: "#1f1f28", Text: "#dcd7ba", Prompt: "#7e9cd8", Markdown: "#dcd7ba", Status: "#957fb8", Diff: "#e6c384", Success: "#98bb6c", Warning: "#e6c384", Error: "#e46876", Accent: "#d27e99", Muted: "#c8c093", Link: "#7fb4ca"},
	{ID: "ayu-dark", Name: "Ayu Dark", Appearance: "Dark", Background: "#10141c", Text: "#bfbdb6", Prompt: "#59c2ff", Markdown: "#bfbdb6", Status: "#d2a6ff", Diff: "#ffb454", Success: "#aad94c", Warning: "#ffb454", Error: "#d95757", Accent: "#e6b450", Muted: "#5a6673", Link: "#95e6cb"},
	{ID: "flexoki-dark", Name: "Flexoki Dark", Appearance: "Dark", Background: "#100f0f", Text: "#fffcf0", Prompt: "#4385be", Markdown: "#fffcf0", Status: "#8b7ec8", Diff: "#d0a215", Success: "#879a39", Warning: "#da702c", Error: "#d14d41", Accent: "#ce5d97", Muted: "#878580", Link: "#3aa99f"},
	{ID: "night-owl", Name: "Night Owl", Appearance: "Dark", Background: "#011627", Text: "#d6deeb", Prompt: "#82aaff", Markdown: "#d6deeb", Status: "#c792ea", Diff: "#ecc48d", Success: "#addb67", Warning: "#f78c6c", Error: "#ef5350", Accent: "#c792ea", Muted: "#637777", Link: "#7fdbca"},
	{ID: "monokai", Name: "Monokai", Appearance: "Dark", Background: "#272822", Text: "#f8f8f2", Prompt: "#66d9ef", Markdown: "#f8f8f2", Status: "#ae81ff", Diff: "#e6db74", Success: "#a6e22e", Warning: "#fd971f", Error: "#f92672", Accent: "#f92672", Muted: "#75715e", Link: "#a1efe4"},
	{ID: "github-dark", Name: "GitHub Dark", Appearance: "Dark", Background: "#0d1117", Text: "#e6edf3", Prompt: "#58a6ff", Markdown: "#e6edf3", Status: "#bc8cff", Diff: "#d29922", Success: "#3fb950", Warning: "#d29922", Error: "#f85149", Accent: "#2f81f7", Muted: "#8b949e", Link: "#79c0ff"},
	{ID: "catppuccin-latte", Name: "Catppuccin Latte", Appearance: "Light", Background: "#eff1f5", Text: "#4c4f69", Prompt: "#1e66f5", Markdown: "#4c4f69", Status: "#8839ef", Diff: "#df8e1d", Success: "#40a02b", Warning: "#df8e1d", Error: "#d20f39", Accent: "#ea76cb", Muted: "#8c8fa1", Link: "#179299"},
	{ID: "alucard", Name: "Alucard", Appearance: "Light", Background: "#fffbeb", Text: "#1f1f1f", Prompt: "#644ac9", Markdown: "#1f1f1f", Status: "#a3144d", Diff: "#846e15", Success: "#14710a", Warning: "#846e15", Error: "#cb3a2a", Accent: "#a3144d", Muted: "#6c664b", Link: "#036a96"},
	{ID: "gruvbox-light", Name: "Gruvbox Light", Appearance: "Light", Background: "#fbf1c7", Text: "#3c3836", Prompt: "#427b58", Markdown: "#3c3836", Status: "#8f3f71", Diff: "#b57614", Success: "#79740e", Warning: "#b57614", Error: "#9d0006", Accent: "#8f3f71", Muted: "#928374", Link: "#427b58"},
	{ID: "solarized-light", Name: "Solarized Light", Appearance: "Light", Background: "#fdf6e3", Text: "#657b83", Prompt: "#268bd2", Markdown: "#657b83", Status: "#6c71c4", Diff: "#b58900", Success: "#859900", Warning: "#b58900", Error: "#dc322f", Accent: "#d33682", Muted: "#93a1a1", Link: "#2aa198"},
	{ID: "nord-light", Name: "Nord Light", Appearance: "Light", Background: "#eceff4", Text: "#2e3440", Prompt: "#5e81ac", Markdown: "#2e3440", Status: "#5e81ac", Diff: "#d08770", Success: "#a3be8c", Warning: "#ebcb8b", Error: "#bf616a", Accent: "#b48ead", Muted: "#7b88a1", Link: "#5e81ac"},
	{ID: "tokyo-night-day", Name: "Tokyo Night Day", Appearance: "Light", Background: "#e1e2e7", Text: "#3760bf", Prompt: "#2e7de9", Markdown: "#3760bf", Status: "#9854f1", Diff: "#8c6c3e", Success: "#587539", Warning: "#8c6c3e", Error: "#f52a65", Accent: "#9854f1", Muted: "#6172b0", Link: "#007197"},
	{ID: "one-light", Name: "One Light", Appearance: "Light", Background: "#fafafa", Text: "#383a42", Prompt: "#4078f2", Markdown: "#383a42", Status: "#a626a4", Diff: "#986801", Success: "#50a14f", Warning: "#986801", Error: "#e45649", Accent: "#a626a4", Muted: "#696c77", Link: "#0184bc"},
	{ID: "rose-pine-dawn", Name: "Rosé Pine Dawn", Appearance: "Light", Background: "#faf4ed", Text: "#464261", Prompt: "#286983", Markdown: "#464261", Status: "#907aa9", Diff: "#ea9d34", Success: "#6d8f89", Warning: "#ea9d34", Error: "#b4637a", Accent: "#d7827e", Muted: "#797593", Link: "#56949f"},
	{ID: "everforest-light", Name: "Everforest Light", Appearance: "Light", Background: "#fffbef", Text: "#5c6a72", Prompt: "#3a94c5", Markdown: "#5c6a72", Status: "#df69ba", Diff: "#dfa000", Success: "#8da101", Warning: "#dfa000", Error: "#f85552", Accent: "#35a77c", Muted: "#829181", Link: "#3a94c5"},
	{ID: "kanagawa-lotus", Name: "Kanagawa Lotus", Appearance: "Light", Background: "#f2ecbc", Text: "#545464", Prompt: "#4d699b", Markdown: "#545464", Status: "#624c83", Diff: "#836f4a", Success: "#6f894e", Warning: "#836f4a", Error: "#c84053", Accent: "#b35b79", Muted: "#716e61", Link: "#597b75"},
	{ID: "ayu-light", Name: "Ayu Light", Appearance: "Light", Background: "#fcfcfc", Text: "#5c6166", Prompt: "#22a4e6", Markdown: "#5c6166", Status: "#a37acc", Diff: "#e59645", Success: "#86b300", Warning: "#e59645", Error: "#e65050", Accent: "#f29718", Muted: "#828e9f", Link: "#55b4d4"},
	{ID: "flexoki-light", Name: "Flexoki Light", Appearance: "Light", Background: "#fffcf0", Text: "#100f0f", Prompt: "#205ea6", Markdown: "#100f0f", Status: "#5e409d", Diff: "#ad8301", Success: "#66800b", Warning: "#bc5215", Error: "#af3029", Accent: "#a02f6f", Muted: "#6f6e69", Link: "#24837b"},
	{ID: "light-owl", Name: "Light Owl", Appearance: "Light", Background: "#fbfbfb", Text: "#403f53", Prompt: "#288ed7", Markdown: "#403f53", Status: "#d6438a", Diff: "#e0af02", Success: "#08916a", Warning: "#e0af02", Error: "#de3d3b", Accent: "#d6438a", Muted: "#7e8296", Link: "#2aa298"},
	{ID: "github-light", Name: "GitHub Light", Appearance: "Light", Background: "#ffffff", Text: "#1f2328", Prompt: "#0969da", Markdown: "#1f2328", Status: "#8250df", Diff: "#9a6700", Success: "#1a7f37", Warning: "#9a6700", Error: "#cf222e", Accent: "#8250df", Muted: "#59636e", Link: "#0969da"},
}

var (
	palettes   = buildPalettes()
	paletteMap = buildPaletteMap()
)

func buildPalettes() []Palette {
	result := make([]Palette, len(rawPalettes))
	for i, p := range rawPalettes {
		result[i] = p
		if p.ID == "default" {
			continue
		}
		result[i].ANSI256 = buildANSI256(p)
	}
	return result
}

func buildPaletteMap() map[string]Palette {
	result := make(map[string]Palette, len(palettes))
	for _, p := range palettes {
		result[p.ID] = p
	}
	return result
}

// buildANSI256 materializes the full xterm-style palette. The first sixteen
// entries follow the theme's semantic roles; the extended cube and grayscale
// ramp are tinted toward the theme so every indexed color belongs to it.
func buildANSI256(p Palette) [256]int32 {
	var table [256]int32
	bg := Color(p.Background)
	text := Color(p.Text)
	muted := Color(p.Muted)
	accent := Color(p.Accent)
	status := Color(p.Status)
	success := Color(p.Success)
	warning := Color(p.Warning)
	err := Color(p.Error)
	link := Color(p.Link)

	base := [6]int32{bg, err, success, warning, status, accent}
	table[0] = bg
	copy(table[1:6], base[1:6])
	table[6] = link
	table[7] = Mix(text, bg, 0.15)
	table[8] = muted
	for i := 1; i < 6; i++ {
		table[8+i] = Mix(base[i], text, 0.20)
	}
	table[11] = Mix(warning, text, 0.20)
	table[14] = Mix(link, text, 0.20)
	table[15] = text

	levels := [6]int32{0, 95, 135, 175, 215, 255}
	index := 16
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				c := RGB(levels[r], levels[g], levels[b])
				c = Mix(c, bg, 0.14)
				c = Mix(c, accent, 0.04)
				table[index] = c
				index++
			}
		}
	}
	for i := 0; i < 24; i++ {
		v := int32(8 + i*10)
		table[232+i] = Mix(RGB(v, v, v), text, 0.10)
	}
	return table
}

// Color parses a #rrggbb string into packed 0xRRGGBB. Invalid values return 0.
func Color(hex string) int32 {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return 0
	}
	return RGB(r, g, b)
}

// RGB packs an 8-bit red, green, blue triplet into 0xRRGGBB.
func RGB(r, g, b int32) int32 {
	return (r&0xff)<<16 | (g&0xff)<<8 | b&0xff
}

// Mix blends a toward b by weight (0..1). Both colors are packed 0xRRGGBB.
func Mix(a, b int32, weight float64) int32 {
	if weight <= 0 {
		return a
	}
	if weight >= 1 {
		return b
	}
	ar, ag, ab := (a>>16)&0xff, (a>>8)&0xff, a&0xff
	br, bg, bb := (b>>16)&0xff, (b>>8)&0xff, b&0xff
	blend := func(x, y int32) int32 {
		return int32(float64(x)*(1-weight) + float64(y)*weight + 0.5)
	}
	return RGB(blend(ar, br), blend(ag, bg), blend(ab, bb))
}

// MixHex blends two #rrggbb colors.
func MixHex(a, b string, weight float64) int32 {
	return Mix(Color(a), Color(b), weight)
}

// Default returns the terminal-default palette, which does not recolor panes.
func Default() Palette { return palettes[0] }

// Lookup finds a palette by ID, ignoring case and surrounding whitespace.
func Lookup(id string) (Palette, bool) {
	p, ok := paletteMap[strings.ToLower(strings.TrimSpace(id))]
	return p, ok
}

// All returns a copy of every palette, Default first.
func All() []Palette { return append([]Palette(nil), palettes...) }

// IDs returns every theme ID in display order.
func IDs() []string {
	ids := make([]string, len(palettes))
	for i, p := range palettes {
		ids[i] = p.ID
	}
	return ids
}

// Labels returns the human-readable setting labels in display order.
func Labels() []string {
	labels := make([]string, len(palettes))
	for i, p := range palettes {
		if p.ID == "default" {
			labels[i] = "Default (terminal)"
			continue
		}
		labels[i] = p.Name + " · " + p.Appearance
	}
	return labels
}

// ValidID reports whether id names a known palette.
func ValidID(id string) bool {
	_, ok := Lookup(id)
	return ok
}

func parseHex(hex string) (r, g, b int32, ok bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	parse := func(s string) (int32, bool) {
		v, err := strconv.ParseInt(s, 16, 0)
		return int32(v), err == nil
	}
	r, okR := parse(hex[1:3])
	g, okG := parse(hex[3:5])
	b, okB := parse(hex[5:7])
	return r, g, b, okR && okG && okB
}
