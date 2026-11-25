package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func exerciseFormsStore(t *testing.T, store formsStore, guild string) {
	t.Helper()
	id, err := store.CreateForm(&appForm{GuildID: guild, Name: "Game jam", ChannelID: "c1", Open: true, MaxPerMember: 1, DMReceipt: true, CreatedBy: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := store.GetForm("other", id); f != nil {
		t.Fatal("form visible from another server")
	}
	f, err := store.GetForm(guild, id)
	if err != nil || f == nil || f.Review || f.MaxPerMember != 1 {
		t.Fatalf("get: %+v %v", f, err)
	}
	f.Review, f.ButtonLabel, f.Header, f.AcceptRoleID = true, "Enter the jam", "Rules", "r9"
	if err := store.UpdateForm(f); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.ListForms(guild); len(list) != 1 || !list[0].Review || list[0].ButtonLabel != "Enter the jam" || list[0].AcceptRoleID != "r9" {
		t.Fatalf("list = %+v", list)
	}

	for _, label := range []string{"Game name", "Link", "Team"} {
		if err := store.AddQuestion(id, formQuestion{Label: label, Kind: kindShort, Required: true}); err != nil {
			t.Fatal(err)
		}
	}
	qs, _ := store.Questions(id)
	if err := store.ReorderQuestions(id, moveID(qs, qs[2].ID, 1)); err != nil {
		t.Fatal(err)
	}
	_ = store.RemoveQuestion(id, qs[0].ID)
	if qs, _ = store.Questions(id); len(qs) != 2 || qs[0].Label != "Team" || qs[1].Label != "Link" {
		t.Fatalf("questions = %+v", qs)
	}

	first, err := store.Submit(&submission{FormID: id, GuildID: guild, UserID: "m1", Answers: `[{"question":"Link","answer":"https://x"}]`, Status: subPending})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := store.Submit(&submission{FormID: id, GuildID: guild, UserID: "m1", Status: subReceived})
	_ = store.SetSubmissionMessage(first, "c1", "msg1")
	sub, _ := store.GetSubmission(guild, first)
	if sub == nil || sub.Status != subPending || sub.MessageID != "msg1" || loadAnswers(sub.Answers)[0].Answer != "https://x" {
		t.Fatalf("submission = %+v", sub)
	}
	if mine, _ := store.SubmissionsOf(id, "m1"); len(mine) != 2 || mine[0].ID != second {
		t.Fatalf("mine, newest first = %+v", mine)
	}

	_ = store.SetVote(first, "s1", "yes")
	_ = store.SetVote(first, "s2", "no")
	_ = store.SetVote(first, "s1", "unsure")
	_ = store.SetVote(first, "s2", "")
	if votes, _ := store.Votes(first); len(votes) != 1 || votes[0].Vote != "unsure" {
		t.Fatalf("votes = %+v", votes)
	}
	if ok, err := store.Decide(first, subAccepted, "s1", "great game"); !ok || err != nil {
		t.Fatalf("decide: %v %v", ok, err)
	}
	if ok, _ := store.Decide(first, subDeclined, "s2", ""); ok {
		t.Fatal("decided twice")
	}
	if ok, _ := store.Decide(second, subAccepted, "s1", ""); ok {
		t.Fatal("a received submission has nothing to decide")
	}
	if accepted, _ := store.ListSubmissions(guild, 0, subAccepted); len(accepted) != 1 || accepted[0].Note != "great game" {
		t.Fatalf("accepted = %+v", accepted)
	}

	if err := store.DeleteForm(id); err != nil {
		t.Fatal(err)
	}
	if sub, _ := store.GetSubmission(guild, first); sub != nil {
		t.Fatal("submissions not deleted with the form")
	}
}

func TestMemFormsStore(t *testing.T) {
	exerciseFormsStore(t, newMemFormsStore(), "g1")
}

func TestPostgresFormsStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}
	defer db.Close()
	store, err := newPostgresFormsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	guild := "test-" + newMarketingID()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM `+tableForms+` WHERE guild_id = $1`, guild)
	})
	exerciseFormsStore(t, store, guild)
}

func TestCanSubmitLimits(t *testing.T) {
	appForms = newMemFormsStore()
	t.Cleanup(func() { appForms = nil })
	f := &appForm{ID: 1, Name: "Jam", Open: true, MaxPerMember: 1}
	if err := canSubmit(f, "m"); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	id, _ := appForms.Submit(&submission{FormID: 1, GuildID: "g", UserID: "m", Status: subPending})
	if err := canSubmit(f, "m"); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("second submission: %v", err)
	}
	_, _ = appForms.Decide(id, subDeclined, "s", "")
	if err := canSubmit(f, "m"); err != nil {
		t.Fatalf("declined ones don't count: %v", err)
	}
	f.MaxPerMember = 0
	_, _ = appForms.Submit(&submission{FormID: 1, GuildID: "g", UserID: "m"})
	if err := canSubmit(f, "m"); err != nil {
		t.Fatalf("no limit: %v", err)
	}
	f.Open = false
	if err := canSubmit(f, "m"); err == nil {
		t.Fatal("a closed form takes nothing")
	}
}

func TestSubmissionMessage(t *testing.T) {
	answers := []formAnswer{{Question: "Game name", Answer: "Blaze Runner"}, {Question: "About", Answer: strings.Repeat("long ", 400)}}
	sub := &submission{ID: 5, UserID: "175928847299117063", Answers: dumpAnswers(answers), Status: subReceived, SubmittedAt: time.Now()}
	member := &discordgo.Member{User: &discordgo.User{Username: "ann"}, JoinedAt: time.Now().Add(-time.Hour)}

	plain := submissionMessage(&appForm{Name: "Game jam"}, sub, member, nil)
	if len(plain.Components) != 0 || strings.Contains(plain.Embeds[0].Description, "Status") {
		t.Fatal("without review: no status, no buttons")
	}
	if len(plain.Files) != 1 || plain.Embeds[0].Fields[0].Value != "Blaze Runner" {
		t.Fatal("answers shown, long ones as a file too")
	}

	sub.Status = subPending
	votes := []appVote{{UserID: "1", Vote: "yes"}, {UserID: "2", Vote: "yes"}}
	review := submissionMessage(&appForm{Name: "Mods", Review: true}, sub, member, votes)
	if len(review.Components) != 2 || !strings.Contains(review.Embeds[0].Description, "waiting for a decision") {
		t.Fatalf("with review: %+v", review)
	}
	accept := review.Components[0].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	yes := review.Components[1].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	if accept.CustomID != "formsub:5:accept" || yes.CustomID != "formsub:5:yes" || yes.Label != "👍 Yes · 2" {
		t.Fatalf("buttons: %+v %+v", accept, yes)
	}

	sub.Status, sub.DecidedBy, sub.Note = subAccepted, "9", "Nice"
	done := submissionMessage(&appForm{Name: "Mods", Review: true}, sub, nil, nil)
	if len(done.Components) != 0 || done.Embeds[0].Color != colorOpened || !strings.Contains(done.Embeds[0].Description, "Nice") {
		t.Fatal("decided: no buttons, shows the decision")
	}
}

func TestVoteTally(t *testing.T) {
	votes := []appVote{{UserID: "1", Vote: "yes"}, {UserID: "2", Vote: "no"}, {UserID: "3", Vote: "yes"}}
	if got := voteTally(votes, false); got != "👍 2 · 👎 1 · 🤷 0" {
		t.Fatalf("tally = %q", got)
	}
	if got := voteTally(votes, true); !strings.Contains(got, "👍 <@1>, <@3>") || !strings.Contains(got, "👎 <@2>") {
		t.Fatalf("named tally = %q", got)
	}
	if got := voteTally(nil, true); !strings.HasSuffix(got, "No votes yet.") {
		t.Fatalf("empty = %q", got)
	}
}
