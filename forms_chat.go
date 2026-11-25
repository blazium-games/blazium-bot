package main

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// Private chats with applicants: a form can open a channel for each applicant once they've
// applied, in a category of its own, that only the applicant, the form's chat staff roles
// (and reviewer role) and the bot can see. Their application is posted there to talk it
// over, and the decision too. Applying again reuses the member's chat if it's still there.
// The bot never deletes these channels; staff tidy them up by hand.

const (
	guildChannelLimit    = 500 // Discord's limits
	categoryChannelLimit = 50
)

// chatTalk is what the applicant, the chat staff and the bot may do in a chat.
const chatTalk = discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory |
	discordgo.PermissionAttachFiles | discordgo.PermissionEmbedLinks

// formChatStaff is the roles that see a form's chats: its chat staff roles and its reviewer role,
// never @everyone or the role accepted applicants get, and only roles that still exist.
func formChatStaff(s *discordgo.Session, guildID string, f *appForm) ([]string, error) {
	ids, err := appForms.ChatRoles(f.ID)
	if err != nil {
		return nil, err
	}
	if f.ReviewerRoleID != "" {
		ids = append(ids, f.ReviewerRoleID)
	}
	existing := map[string]bool{}
	if g, err := lookupGuild(s, guildID); err == nil {
		for _, r := range g.Roles {
			existing[r.ID] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id == "" || id == guildID || id == f.AcceptRoleID || seen[id] || (len(existing) > 0 && !existing[id]) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func guildChannels(s *discordgo.Session, guildID string) []*discordgo.Channel {
	if g, err := s.State.Guild(guildID); err == nil && len(g.Channels) > 0 {
		return g.Channels
	}
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return nil
	}
	return channels
}

func categoryChannelCount(s *discordgo.Session, guildID, categoryID string) int {
	n := 0
	for _, ch := range guildChannels(s, guildID) {
		if ch.ParentID == categoryID {
			n++
		}
	}
	return n
}

// openApplicantChat is the applicant's chat for this form: their earlier one if it's still
// there, else a new one. It returns the channel ID, or "" and why not.
func openApplicantChat(s *discordgo.Session, f *appForm, entryID int64, member *discordgo.Member) (string, string) {
	if member == nil || member.User == nil {
		return "", "I couldn't tell who you are"
	}
	earlier, err := appForms.EarlierChats(f.ID, member.User.ID)
	if err != nil {
		appLogger.Warnf("Could not look up earlier chats: %v", err)
	}
	channels := guildChannels(s, f.GuildID)
	present := map[string]bool{}
	inCategory := 0
	for _, ch := range channels {
		present[ch.ID] = true
		if ch.ParentID == f.ChatCategoryID {
			inCategory++
		}
	}
	for _, id := range earlier {
		if present[id] {
			if err := appForms.SetChat(entryID, id); err != nil {
				appLogger.Warnf("Could not save chat %s: %v", id, err)
			}
			return id, ""
		}
	}
	if !present[f.ChatCategoryID] {
		return "", "the form's chat category is gone"
	}
	if len(channels) >= guildChannelLimit {
		return "", fmt.Sprintf("the server is at Discord's limit of %d channels", guildChannelLimit)
	}
	if inCategory >= categoryChannelLimit {
		return "", fmt.Sprintf("the chat category is full (%d channels)", categoryChannelLimit)
	}
	staff, err := formChatStaff(s, f.GuildID, f)
	if err != nil || len(staff) == 0 {
		return "", "the form has no chat staff roles"
	}
	overwrites := []*discordgo.PermissionOverwrite{
		{ID: f.GuildID, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
		{ID: s.State.User.ID, Type: discordgo.PermissionOverwriteTypeMember, Allow: chatTalk},
		{ID: member.User.ID, Type: discordgo.PermissionOverwriteTypeMember, Allow: chatTalk},
	}
	for _, id := range staff {
		overwrites = append(overwrites, &discordgo.PermissionOverwrite{ID: id, Type: discordgo.PermissionOverwriteTypeRole, Allow: chatTalk})
	}
	ch, err := s.GuildChannelCreateComplex(f.GuildID, discordgo.GuildChannelCreateData{
		Name:                 chatName(member.User.Username, member.User.ID),
		Type:                 discordgo.ChannelTypeGuildText,
		Topic:                truncateRunes(fmt.Sprintf("Application chat for %s (%s) · %s", member.User.Username, member.User.ID, f.Name), 1024),
		ParentID:             f.ChatCategoryID,
		PermissionOverwrites: overwrites,
	}, discordgo.WithAuditLogReason(truncateRunes(fmt.Sprintf("Applications: %s applied to %s", member.User.Username, f.Name), 500)))
	if err != nil {
		appLogger.Warnf("Could not open an application chat: %v", err)
		return "", "Discord refused to make the channel"
	}
	if err := appForms.SetChat(entryID, ch.ID); err != nil {
		appLogger.Warnf("Could not save chat %s: %v", ch.ID, err)
	}
	return ch.ID, ""
}

// postInChat posts the application in the applicant's chat, then the staff's welcome message if any.
func postInChat(s *discordgo.Session, f *appForm, entryID int64, chatID string) {
	e, err := appForms.GetEntry(f.GuildID, entryID)
	if err != nil || e == nil {
		return
	}
	staff, _ := formChatStaff(s, f.GuildID, f)
	msg := reviewMessage(s, f, e, nil, false)
	msg.Content = fmt.Sprintf("<@%s>, thanks for applying to **%s**! This chat is just you, %s and me. Staff may ask you questions here; you'll hear the decision here and by DM.",
		e.UserID, f.Name, mentionAll(staff))
	pingApplicant := &discordgo.MessageAllowedMentions{Users: []string{e.UserID}}
	msg.AllowedMentions = pingApplicant
	if _, err := s.ChannelMessageSendComplex(chatID, msg); err != nil {
		appLogger.Warnf("Could not post application %d in its chat: %v", entryID, err)
		return
	}
	if f.Welcome == "" {
		return
	}
	name := "there"
	if m := lookupMember(s, f.GuildID, e.UserID); m != nil && m.User != nil {
		name = m.User.Username
		if m.Nick != "" {
			name = m.Nick
		} else if m.User.GlobalName != "" {
			name = m.User.GlobalName
		}
	}
	text := fillWelcome(f.Welcome, "<@"+e.UserID+">", name, f.Name)
	if _, err := s.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{Content: text, AllowedMentions: pingApplicant}); err != nil {
		appLogger.Warnf("Could not post the welcome message for application %d: %v", entryID, err)
	}
}
