package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	marketingEmbedNewColor      = 0x3BA55D
	marketingEmbedOutreachColor = 0xF0B232
)

var (
	onProspectCreated  = defaultOnProspectCreated
	onOutreachRecorded = defaultOnOutreachRecorded
	marketingDiscordPoster func(embed *discordgo.MessageEmbed) (string, error)
)

func defaultOnProspectCreated(p *MarketingProspect) {
	go postNewProspectEmbed(p)
}

func defaultOnOutreachRecorded(p *MarketingProspect, o *MarketingOutreach) {
	go postOutreachEmbed(p, o)
}

func postNewProspectEmbed(p *MarketingProspect) {
	defer func() {
		if rec := recover(); rec != nil {
			appLogger.Errorf("marketing discord new-entry panic: %v", rec)
		}
	}()
	if p == nil {
		return
	}
	if _, err := sendMarketingEmbed(buildNewProspectEmbed(p)); err != nil {
		appLogger.Errorf("marketing discord new-entry embed: %v", err)
	}
}

func postOutreachEmbed(p *MarketingProspect, o *MarketingOutreach) {
	defer func() {
		if rec := recover(); rec != nil {
			appLogger.Errorf("marketing discord outbound panic: %v", rec)
		}
	}()
	if p == nil || o == nil {
		return
	}
	msgID, err := sendMarketingEmbed(buildOutreachEmbed(p, o))
	if err != nil {
		appLogger.Errorf("marketing discord outbound embed: %v", err)
		return
	}
	if msgID != "" && marketingStore != nil {
		if err := marketingStore.UpdateOutreachDiscordID(o.ID, msgID); err != nil {
			appLogger.Errorf("marketing discord message id save: %v", err)
		}
	}
}

func sendMarketingEmbed(embed *discordgo.MessageEmbed) (string, error) {
	if embed == nil {
		return "", nil
	}
	if marketingDiscordPoster != nil {
		return marketingDiscordPoster(embed)
	}
	session := marketingDiscordSession()
	if session == nil {
		return "", fmt.Errorf("discord session unavailable")
	}
	channelID := marketingCfg.channelID
	if channelID == "" {
		channelID = defaultMarketingChannelID
	}
	msg, err := session.ChannelMessageSendEmbed(channelID, embed)
	if err != nil {
		return "", err
	}
	if msg == nil {
		return "", nil
	}
	return msg.ID, nil
}

func marketingDiscordSession() *discordgo.Session {
	if CommandManager == nil {
		return nil
	}
	sm := CommandManager.GetShardManager()
	if sm == nil {
		return nil
	}
	return sm.SessionForDM()
}

func buildNewProspectEmbed(p *MarketingProspect) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:     "New marketing prospect",
		Color:     marketingEmbedNewColor,
		Timestamp: p.CreatedAt.UTC().Format(time.RFC3339),
		Fields: []*discordgo.MessageEmbedField{
			embedField("Name", displayOrDash(p.DisplayName), true),
			embedField("Email", displayOrDash(p.Email), true),
			embedField("Project", displayOrDash(p.ProjectName), true),
			embedField("Persona", displayOrDash(p.Persona), true),
			embedField("Need", displayOrDash(p.Need), true),
			embedField("Source", displayOrDash(p.Source), true),
			embedField("Campaign", displayOrDash(p.Campaign), false),
			embedField("Public URL", displayOrDash(p.PublicURL), false),
			embedField("Status", displayOrDash(p.Status), true),
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Grokbot · prospect %s", p.ID),
		},
	}
}

func buildOutreachEmbed(p *MarketingProspect, o *MarketingOutreach) *discordgo.MessageEmbed {
	to := strings.TrimSpace(o.DisplayName)
	if to == "" {
		to = displayOrDash(o.Email)
	} else if o.Email != "" {
		to = to + " <" + o.Email + ">"
	}
	when := o.OccurredAt.UTC().Format(time.RFC3339)
	return &discordgo.MessageEmbed{
		Title:     "Outreach sent",
		Color:     marketingEmbedOutreachColor,
		Timestamp: when,
		Fields: []*discordgo.MessageEmbedField{
			embedField("To", to, false),
			embedField("Campaign", displayOrDash(o.Campaign), true),
			embedField("Channel", displayOrDash(o.Channel), true),
			embedField("Subject", displayOrDash(o.Subject), false),
			embedField("Touch", fmt.Sprintf("%d / %d", o.TouchNumber, marketingTouchCap), true),
			embedField("Project", displayOrDash(p.ProjectName), true),
			embedField("Public URL", displayOrDash(p.PublicURL), false),
			embedField("When", when, true),
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Grokbot · outreach %s", o.ID),
		},
	}
}

func embedField(name, value string, inline bool) *discordgo.MessageEmbedField {
	return &discordgo.MessageEmbedField{
		Name:   name,
		Value:  value,
		Inline: inline,
	}
}

func displayOrDash(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "—"
	}
	return v
}
