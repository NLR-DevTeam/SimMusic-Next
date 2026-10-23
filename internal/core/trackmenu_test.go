package core

import (
	"testing"
	"time"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

func TestPlayerTrackMenuValidatesListAndSelection(t *testing.T) {
	a, p := musicFixture(t, `exports.getMusicInfo=()=>{throw new Error("menu must not fetch metadata")}; exports.menu=(list,selected)=>sim.player.showTrackMenu(list,selected);`, nil)
	p.Manifest.Permissions = append(p.Manifest.Permissions, plugin.PermPlayer)
	a.Plugins.Host = a
	var list, chosen []model.Track
	a.OnTrackMenu = func(all, selected []model.Track) { list, chosen = all, selected }
	refs := []model.Track{{Source: "test", ID: "1", Title: "injected", Cover: "file:C:/secret"}, {Source: "test", ID: "2"}}
	if err := a.PlayerTrackMenu(p, refs, []model.Track{{Source: "test", ID: "2"}}); err != nil {
		t.Fatal(err)
	}
	var accepted bool
	if err := p.CallInto(time.Second, &accepted, "menu", refs, []model.Track{{Source: "test", ID: "2"}}); err != nil || !accepted {
		t.Fatalf("Goja menu bridge: %v %v", accepted, err)
	}
	if len(list) != 2 || len(chosen) != 1 || chosen[0].ID != "2" || list[0].Title != "" || list[0].Cover != "" {
		t.Fatalf("invalid menu payload: %+v %+v", list, chosen)
	}
	for _, bad := range []struct{ list, selected []model.Track }{
		{refs, []model.Track{{Source: "test", ID: "3"}}},
		{[]model.Track{{Source: "other", ID: "1"}}, []model.Track{{Source: "other", ID: "1"}}},
		{refs, nil}, {nil, nil}, {[]model.Track{{Source: "test"}}, []model.Track{{Source: "test"}}},
	} {
		if err := a.PlayerTrackMenu(p, bad.list, bad.selected); err == nil {
			t.Fatal("invalid menu request accepted", bad)
		}
	}
	p.Manifest.Permissions = []string{plugin.PermNetwork}
	if err := a.PlayerTrackMenu(p, refs, refs[:1]); err == nil {
		t.Fatal("revoked player permission accepted")
	}
}
