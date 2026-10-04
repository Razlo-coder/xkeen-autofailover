package xkeen

import "testing"

func TestNumberedGroupNameRecognisesFlattenedProfiles(t *testing.T) {
	cases := map[string]string{
		"⚡ Авто · 1":          "⚡ Авто",
		"⚡ Авто · 8":          "⚡ Авто",
		"⚡ Авто+ · 1":         "⚡ Авто+",
		"Auto · 3":            "⚡ Auto",
		"🇫🇮 Финляндия+ · 1":   "🇫🇮 Финляндия+",
		"🇫🇮 Финляндия + · 10": "🇫🇮 Финляндия+",
		"Германия+ • 2":       "Германия+",
		"NL Амстердам":        "",
		"Германия · 0":        "",
		"Германия · не число": "",
	}
	for name, want := range cases {
		if got := NumberedGroupName(name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}
