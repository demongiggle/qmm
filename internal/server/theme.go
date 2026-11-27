package server

import (
	"math"

	"github.com/demongiggle/qmm/internal/protocol"
	"github.com/demongiggle/qmm/internal/theme"
)

// helpColors is the palette shared by the help, settings, sessions, warning,
// and shutdown dialogs.
type helpColors struct {
	backdrop, panelBG, border, title, header int32
	keyFG, keyBG, fg, muted                  int32
}

// paneNumberColors is the palette used by the prefix+q pane-number overlay.
type paneNumberColors struct {
	panelBG, panelDimBG    int32
	border, borderDim      int32
	glyph, glyphDim        int32
	activeBG, activeBorder int32
	activeFG               int32
	shadowBG               int32
	hintFG, hintBG         int32
}

// chrome holds every color used outside pane output. The default chrome
// reproduces qmm's original constants; a named theme derives the same fields
// from its qcode palette.
type chrome struct {
	themed     bool
	background int32
	text       int32
	ansi       [256]int32

	border       int32
	activeBorder int32
	activeFG     int32
	tooSmall     int32

	statusBar  statusBarStyle
	copyStatus statusBarStyle

	help         helpColors
	paneNumber   paneNumberColors
	settingsFG   int32
	settingsBG   int32
	copySelFG    int32
	copySelBG    int32
	copyCursorFG int32
	copyCursorBG int32
}

func defaultChrome() chrome {
	return chrome{
		background:   colDefault,
		text:         colDefault,
		border:       colBorder,
		activeBorder: accentColorCodes[0],
		activeFG:     colBarActiveFG,
		tooSmall:     colTooSmall,
		statusBar:    statusBarColors[0],
		copyStatus:   statusBarColors[1],
		help: helpColors{
			backdrop: colHelpBackdrop,
			panelBG:  colHelpPanelBG,
			border:   colHelpBorder,
			title:    colHelpTitle,
			header:   colHelpHeader,
			keyFG:    colHelpKeyFG,
			keyBG:    colHelpKeyBG,
			fg:       colHelpFG,
			muted:    colHelpMuted,
		},
		paneNumber: paneNumberColors{
			panelBG:      colPaneNumberPanelBG,
			panelDimBG:   colPaneNumberPanelDimBG,
			border:       colPaneNumberBorder,
			borderDim:    colPaneNumberBorderDim,
			glyph:        colPaneNumberGlyph,
			glyphDim:     colPaneNumberGlyphDim,
			activeBG:     colPaneNumberActiveBG,
			activeBorder: colPaneNumberActiveBord,
			activeFG:     colPaneNumberActiveFG,
			shadowBG:     colPaneNumberShadowBG,
			hintFG:       colPaneNumberHintFG,
			hintBG:       colPaneNumberHintBG,
		},
		settingsFG:   colSettingsSelectedFG,
		settingsBG:   colSettingsSelectedBG,
		copySelFG:    colCopySelectionFG,
		copySelBG:    colCopySelectionBG,
		copyCursorFG: colCopyCursorFG,
		copyCursorBG: colCopyCursorBG,
	}
}

func chromeForPalette(p theme.Palette) chrome {
	if p.ID == "" || p.ID == "default" {
		return defaultChrome()
	}
	bg := theme.Color(p.Background)
	text := theme.Color(p.Text)
	accent := theme.Color(p.Accent)
	status := theme.Color(p.Status)
	success := theme.Color(p.Success)
	warning := theme.Color(p.Warning)
	errColor := theme.Color(p.Error)
	muted := theme.Mix(text, bg, 0.35)
	panel := theme.Mix(bg, text, 0.08)
	backdrop := theme.Mix(bg, text, 0.18)
	border := theme.Mix(bg, text, 0.45)
	statusBG := theme.Mix(bg, text, 0.10)
	copyBG := theme.Mix(bg, status, 0.22)
	activeFG := readable(bg, accent, text)

	help := helpColors{
		backdrop: backdrop,
		panelBG:  panel,
		border:   readable(status, panel, text),
		title:    readable(success, panel, text),
		header:   readable(status, panel, text),
		keyFG:    readable(warning, theme.Mix(panel, text, 0.12), text),
		keyBG:    theme.Mix(panel, text, 0.12),
		fg:       readable(text, panel, text),
		muted:    readable(muted, panel, text),
	}
	selectionBG := theme.Mix(bg, status, 0.25)
	settingsBG := theme.Mix(panel, text, 0.20)

	return chrome{
		themed:       true,
		background:   protocolRGB(bg),
		text:         protocolRGB(text),
		ansi:         p.ANSI256,
		border:       protocolRGB(border),
		activeBorder: protocolRGB(accent),
		activeFG:     protocolRGB(activeFG),
		tooSmall:     protocolRGB(readable(errColor, bg, text)),
		statusBar: statusBarStyle{
			bg:     protocolRGB(statusBG),
			fg:     protocolRGB(readable(muted, statusBG, text)),
			accent: protocolRGB(readable(accent, statusBG, text)),
		},
		copyStatus: statusBarStyle{
			bg:     protocolRGB(copyBG),
			fg:     protocolRGB(readable(text, copyBG, text)),
			accent: protocolRGB(readable(success, copyBG, text)),
		},
		help: helpColors{
			backdrop: protocolRGB(backdrop),
			panelBG:  protocolRGB(panel),
			border:   protocolRGB(help.border),
			title:    protocolRGB(help.title),
			header:   protocolRGB(help.header),
			keyFG:    protocolRGB(help.keyFG),
			keyBG:    protocolRGB(help.keyBG),
			fg:       protocolRGB(help.fg),
			muted:    protocolRGB(help.muted),
		},
		paneNumber: paneNumberColors{
			panelBG:      protocolRGB(panel),
			panelDimBG:   protocolRGB(backdrop),
			border:       protocolRGB(help.border),
			borderDim:    protocolRGB(help.muted),
			glyph:        protocolRGB(help.header),
			glyphDim:     protocolRGB(help.muted),
			activeBG:     protocolRGB(panel),
			activeBorder: protocolRGB(readable(accent, panel, text)),
			activeFG:     protocolRGB(readable(accent, panel, text)),
			shadowBG:     protocolRGB(theme.Mix(bg, 0x000000, 0.45)),
			hintFG:       protocolRGB(readable(bg, accent, text)),
			hintBG:       protocolRGB(accent),
		},
		settingsFG:   protocolRGB(readable(text, settingsBG, text)),
		settingsBG:   protocolRGB(settingsBG),
		copySelFG:    protocolRGB(readable(text, selectionBG, text)),
		copySelBG:    protocolRGB(selectionBG),
		copyCursorFG: protocolRGB(readable(bg, accent, text)),
		copyCursorBG: protocolRGB(accent),
	}
}

// protocolRGB converts packed 0xRRGGBB to the protocol's truecolor encoding.
func protocolRGB(c int32) int32 {
	return protocol.RGBColor((c>>16)&0xff, (c>>8)&0xff, c&0xff)
}

// mapColor maps a pane cell color into the active theme. Palette indexes are
// replaced by the theme's materialized 256-color table; default colors become
// the theme's text/background; truecolor RGB cells are left alone.
func (c chrome) mapColor(col int32, fg bool) int32 {
	if !c.themed {
		return col
	}
	if col == protocol.ColorDefault {
		if fg {
			return c.text
		}
		return c.background
	}
	if col >= 0 && col <= 255 {
		return protocolRGB(c.ansi[col])
	}
	return col
}

// readable returns preferred when it has enough contrast against bg; otherwise
// it falls back to fallback.
func readable(preferred, bg, fallback int32) int32 {
	if contrastRatio(preferred, bg) >= 3.0 {
		return preferred
	}
	return fallback
}

func contrastRatio(a, b int32) float64 {
	la := relativeLuminance(a)
	lb := relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(c int32) float64 {
	r := channelLuminance(float64((c >> 16) & 0xff))
	g := channelLuminance(float64((c >> 8) & 0xff))
	b := channelLuminance(float64(c & 0xff))
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func channelLuminance(v float64) float64 {
	v /= 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}
