package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleMarketingCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !hasStaffAccess(s, i) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "This command is limited to staff.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	if marketingStore == nil {
		marketingFollowup(s, i, "Marketing store is not connected.")
		return
	}

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		marketingFollowup(s, i, "Choose a subcommand: stats, lookup, recent, campaigns, or outreach.")
		return
	}

	switch options[0].Name {
	case "stats":
		handleMarketingStatsCommand(s, i)
	case "lookup":
		handleMarketingLookupCommand(s, i)
	case "recent":
		handleMarketingRecentCommand(s, i)
	case "campaigns":
		handleMarketingCampaignsCommand(s, i)
	case "outreach":
		handleMarketingOutreachCommand(s, i)
	default:
		marketingFollowup(s, i, "Unknown subcommand.")
	}
}

func handleMarketingStatsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	st, err := marketingStore.Stats()
	if err != nil {
		marketingFollowup(s, i, "Could not load marketing stats.")
		return
	}
	last := "none"
	if st.LastOutreachAt != nil {
		last = st.LastOutreachAt.UTC().Format(time.RFC3339)
	}
	embed := &discordgo.MessageEmbed{
		Title:       "Marketing stats",
		Color:       marketingEmbedNewColor,
		Description: fmt.Sprintf("Prospects **%d** · Outreach **%d**\nLast send: %s", st.Prospects, st.Outreach, last),
		Fields: []*discordgo.MessageEmbedField{
			embedField("Status", formatCountMap(st.ByStatus, 8), true),
			embedField("Events", formatCountMap(st.EventsByType, 8), true),
			embedField("Need", formatCountMap(st.ByNeed, 8), true),
			embedField("Persona", formatCountMap(st.ByPersona, 8), true),
			embedField("Campaigns", formatCountMap(st.ByCampaign, 8), false),
		},
		Footer: &discordgo.MessageEmbedFooter{Text: "Grokbot marketing"},
	}
	marketingFollowupEmbed(s, i, embed)
}

func handleMarketingLookupCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	id := strings.TrimSpace(GetStringOption(i, "id"))
	email := strings.TrimSpace(GetStringOption(i, "email"))
	publicURL := strings.TrimSpace(GetStringOption(i, "public_url"))
	if id == "" && email == "" && publicURL == "" {
		marketingFollowup(s, i, "Provide id, email, or public_url.")
		return
	}
	p, err := lookupProspect(marketingStore, id, email, publicURL)
	if err != nil || p == nil {
		marketingFollowup(s, i, "No marketing entry found.")
		return
	}
	rows, _ := marketingStore.ListOutreach(outreachFilters{ProspectID: p.ID, Limit: 5})
	marketingFollowupEmbed(s, i, buildMarketingInfoEmbed(p, rows))
}

func handleMarketingRecentCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	limit := GetIntOption(i, "limit")
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	rows, err := marketingStore.ListOutreach(outreachFilters{Limit: limit})
	if err != nil {
		marketingFollowup(s, i, "Could not load recent outreach.")
		return
	}
	if len(rows) == 0 {
		marketingFollowup(s, i, "No outreach recorded yet.")
		return
	}
	marketingFollowup(s, i, formatOutreachList(rows))
}

func handleMarketingCampaignsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	st, err := marketingStore.Stats()
	if err != nil {
		marketingFollowup(s, i, "Could not load campaign stats.")
		return
	}
	embed := &discordgo.MessageEmbed{
		Title:  "Marketing campaigns",
		Color:  marketingEmbedOutreachColor,
		Fields: []*discordgo.MessageEmbedField{embedField("Prospects by campaign", formatCountMap(st.ByCampaign, 15), false)},
	}
	marketingFollowupEmbed(s, i, embed)
}

func handleMarketingOutreachCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	email := strings.TrimSpace(GetStringOption(i, "email"))
	id := strings.TrimSpace(GetStringOption(i, "id"))
	if email == "" && id == "" {
		marketingFollowup(s, i, "Provide email or id.")
		return
	}
	if id != "" && email == "" {
		p, err := marketingStore.GetProspect(id)
		if err != nil || p == nil {
			marketingFollowup(s, i, "No marketing entry found.")
			return
		}
		email = p.Email
		id = p.ID
	}
	rows, err := marketingStore.ListOutreach(outreachFilters{Email: email, ProspectID: id, Limit: 20})
	if err != nil {
		marketingFollowup(s, i, "Could not load outreach history.")
		return
	}
	if len(rows) == 0 {
		marketingFollowup(s, i, "No outreach for that entry.")
		return
	}
	marketingFollowup(s, i, formatOutreachList(rows))
}

func buildMarketingInfoEmbed(p *MarketingProspect, outreach []MarketingOutreach) *discordgo.MessageEmbed {
	last := "none"
	if p.LastOutreachAt != nil {
		last = p.LastOutreachAt.UTC().Format(time.RFC3339)
	}
	desc := formatOutreachList(outreach)
	if desc == "" {
		desc = "No outreach yet."
	}
	return &discordgo.MessageEmbed{
		Title:       "Marketing entry",
		Color:       marketingEmbedNewColor,
		Description: desc,
		Fields: []*discordgo.MessageEmbedField{
			embedField("Name", displayOrDash(p.DisplayName), true),
			embedField("Email", displayOrDash(p.Email), true),
			embedField("Status", displayOrDash(p.Status), true),
			embedField("Project", displayOrDash(p.ProjectName), true),
			embedField("Persona", displayOrDash(p.Persona), true),
			embedField("Need", displayOrDash(p.Need), true),
			embedField("Campaign", displayOrDash(p.Campaign), false),
			embedField("Public URL", displayOrDash(p.PublicURL), false),
			embedField("Touches", fmt.Sprintf("%d / %d", p.TouchCount, marketingTouchCap), true),
			embedField("Last outreach", last, true),
			embedField("ID", p.ID, false),
		},
		Footer: &discordgo.MessageEmbedFooter{Text: "Grokbot marketing"},
	}
}

func formatOutreachList(rows []MarketingOutreach) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, o := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		who := strings.TrimSpace(o.DisplayName)
		if who == "" {
			who = displayOrDash(o.Email)
		} else if o.Email != "" {
			who = who + " <" + o.Email + ">"
		}
		fmt.Fprintf(&b, "`%s` %s · %s · touch %d · %s",
			o.OccurredAt.UTC().Format("2006-01-02 15:04"),
			who,
			displayOrDash(o.Campaign),
			o.TouchNumber,
			displayOrDash(o.Subject),
		)
	}
	out := b.String()
	if len(out) > 1800 {
		return out[:1800] + "…"
	}
	return out
}

func marketingFollowup(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if content == "" {
		content = "No data."
	}
	if len(content) > 1900 {
		content = content[:1900] + "…"
	}
	_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
	if err != nil {
		appLogger.Errorf("marketing followup failed: %v", err)
	}
}

func marketingFollowupEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	})
	if err != nil {
		appLogger.Errorf("marketing followup embed failed: %v", err)
	}
}
