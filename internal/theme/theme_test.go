package theme

import "testing"

func TestPaletteCatalog(t *testing.T) {
	all := All()
	if len(all) != 30 {
		t.Fatalf("palette count = %d, want default + 29 themes", len(all))
	}
	if all[0].ID != "default" {
		t.Fatalf("first palette = %q, want default", all[0].ID)
	}
	seen := map[string]bool{}
	dark, light := 0, 0
	for _, p := range all {
		if seen[p.ID] {
			t.Errorf("duplicate palette ID %q", p.ID)
		}
		seen[p.ID] = true
		switch p.Appearance {
		case "Dark":
			dark++
		case "Light":
			light++
		}
		if p.ID != "default" {
			for name, hex := range map[string]string{
				"background": p.Background, "text": p.Text, "prompt": p.Prompt,
				"status": p.Status, "success": p.Success, "warning": p.Warning,
				"error": p.Error, "accent": p.Accent, "muted": p.Muted, "link": p.Link,
			} {
				if _, _, _, ok := parseHex(hex); !ok {
					t.Errorf("%s %s color %q is invalid", p.ID, name, hex)
				}
			}
		}
	}
	if dark != 15 || light != 14 {
		t.Fatalf("appearance counts = %d dark / %d light, want 15/14", dark, light)
	}

	if p, ok := Lookup("  DRACULA "); !ok || p.Name != "Dracula" {
		t.Fatalf("Lookup(dracula) = %+v, %v", p, ok)
	}
	if _, ok := Lookup("not-a-theme"); ok {
		t.Fatal("Lookup accepted an unknown theme")
	}
	if !ValidID("catppuccin-latte") || ValidID("missing") {
		t.Fatal("ValidID mismatch")
	}
	if got := Labels()[0]; got != "Default (terminal)" {
		t.Fatalf("default label = %q", got)
	}
	if got := Labels()[1]; got != "Catppuccin Mocha · Dark" {
		t.Fatalf("first theme label = %q", got)
	}
}

func TestANSI256Materialization(t *testing.T) {
	if got := Default().ANSI256; got != ([256]int32{}) {
		t.Fatal("default theme should not override pane colors")
	}

	for _, p := range All()[1:] {
		if got, want := p.ANSI256[0], Color(p.Background); got != want {
			t.Errorf("%s ANSI 0 = %#x, want background %#x", p.ID, got, want)
		}
		if got, want := p.ANSI256[1], Color(p.Error); got != want {
			t.Errorf("%s ANSI 1 = %#x, want error %#x", p.ID, got, want)
		}
		if got, want := p.ANSI256[2], Color(p.Success); got != want {
			t.Errorf("%s ANSI 2 = %#x, want success %#x", p.ID, got, want)
		}
		if got, want := p.ANSI256[15], Color(p.Text); got != want {
			t.Errorf("%s ANSI 15 = %#x, want text %#x", p.ID, got, want)
		}
		if got, want := p.ANSI256[232], Mix(RGB(8, 8, 8), Color(p.Text), 0.10); got != want {
			t.Errorf("%s ANSI 232 = %#x, want %#x", p.ID, got, want)
		}
		if got, want := p.ANSI256[255], Mix(RGB(238, 238, 238), Color(p.Text), 0.10); got != want {
			t.Errorf("%s ANSI 255 = %#x, want %#x", p.ID, got, want)
		}
		// The extended cube is tinted toward this theme, not the raw xterm
		// value, and every entry is populated.
		raw196 := RGB(255, 0, 0)
		tinted196 := Mix(Mix(raw196, Color(p.Background), 0.14), Color(p.Accent), 0.04)
		if got := p.ANSI256[196]; got != tinted196 {
			t.Errorf("%s ANSI 196 = %#x, want tinted %#x", p.ID, got, tinted196)
		}
		for i, c := range p.ANSI256 {
			if c == 0 && i != 0 {
				t.Errorf("%s ANSI %d is zero", p.ID, i)
			}
		}
	}

	mocha, _ := Lookup("catppuccin-mocha")
	latte, _ := Lookup("catppuccin-latte")
	if mocha.ANSI256[128] == latte.ANSI256[128] {
		t.Fatal("dark and light themes produced identical extended colors")
	}
}

func TestMixAndColor(t *testing.T) {
	if got, want := Mix(RGB(0, 0, 0), RGB(255, 255, 255), 0.5), RGB(128, 128, 128); got != want {
		t.Fatalf("Mix = %#x, want %#x", got, want)
	}
	if got, want := MixHex("#000000", "#ffffff", 0), Color("#000000"); got != want {
		t.Fatalf("MixHex weight 0 = %#x, want %#x", got, want)
	}
	if got, want := MixHex("#000000", "#ffffff", 1), Color("#ffffff"); got != want {
		t.Fatalf("MixHex weight 1 = %#x, want %#x", got, want)
	}
	if got := Color("#xyz"); got != 0 {
		t.Fatalf("invalid Color = %#x, want 0", got)
	}
}
