package xkeen

import "testing"

func TestAutoGroupNameOnlyMatchesNumberedAutoMembers(t *testing.T) {
	cases := map[string]string{
		"⚡ Авто · 1":   "⚡ Авто",
		"⚡ Авто · 8":   "⚡ Авто",
		"⚡ Авто+ · 1":  "⚡ Авто+",
		"Auto · 3":     "⚡ Auto",
		"NL Амстердам": "",
		"Германия · 1": "",
	}
	for name, want := range cases {
		if got := AutoGroupName(name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}
