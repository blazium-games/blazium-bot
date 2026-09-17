package main

import (
	"reflect"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCollectGuildIDs(t *testing.T) {
	guilds := []*discordgo.Guild{
		{ID: "111", Name: "Blazium Games"},
		{ID: "", Name: "empty"},
		nil,
		{ID: "222", Name: "BlaziumCore"},
		{ID: "333", Name: "down", Unavailable: true},
		{ID: "111", Name: "duplicate"},
	}
	got := collectGuildIDs(guilds, " 1294004245489782896 ")
	want := []string{"111", "222", "1294004245489782896"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectGuildIDsExtraAlreadyPresent(t *testing.T) {
	guilds := []*discordgo.Guild{{ID: "1294004245489782896", Name: "Blazium Games"}}
	got := collectGuildIDs(guilds, "1294004245489782896")
	want := []string{"1294004245489782896"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectGuildIDsNone(t *testing.T) {
	got := collectGuildIDs(nil, "")
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}
