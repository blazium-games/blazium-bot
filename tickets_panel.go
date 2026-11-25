package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// /ticket panel: one message with buttons for ticket types and forms side by side (Support,
// Bug report, Game jam entry…), plus 🔎 Mine, which lists the member's open tickets and
// submissions. Staff first pick the buttons from a menu (tktpanel:pick:<token>), then
// press Post the panel (tktpanel:post:<token>); that menu is kept in memory for 15 minutes.

const panelMaxButtons = 24 // plus 🔎 Mine: Discord allows 25 buttons on a message

// panelItem is a ticket type or a form on a panel.
type panelItem struct {
	Kind  string // "t" for a ticket type, "f" for a form
	ID    int64
	Name  string
	Emoji string
	Open  bool
	form  appForm
	ttype ticketType
}

func (p panelItem) key() string { return fmt.Sprintf("%s:%d", p.Kind, p.ID) }

func ticketItem(tt ticketType) panelItem {
	return panelItem{Kind: "t", ID: tt.ID, Name: tt.Name, Emoji: tt.Emoji, Open: tt.Open, ttype: tt}
}

func formItem(f appForm) panelItem {
	return panelItem{Kind: "f", ID: f.ID, Name: f.Name, Emoji: f.Emoji, Open: f.Open, form: f}
}

// panelButtons is one button per item, then 🔎 Mine, five to a row.
func panelButtons(items []panelItem, onPanel bool) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent
	hasTickets, hasForms := false, false
	for _, it := range items {
		if it.Kind == "f" {
			hasForms = true
			buttons = append(buttons, formFillButton(it.form, true))
		} else {
			hasTickets = true
			buttons = append(buttons, openButton(it.ttype))
		}
	}
	label := "🔎 My tickets"
	switch {
	case hasForms && hasTickets:
		label = "🔎 My tickets & submissions"
	case hasForms:
		label = "🔎 My submissions"
	}
	buttons = append(buttons, discordgo.Button{Label: label, Style: discordgo.SecondaryButton, CustomID: "tickets:mine"})
	var rows []discordgo.MessageComponent
	for len(buttons) > 0 {
		n := min(5, len(buttons))
		rows = append(rows, discordgo.ActionsRow{Components: buttons[:n]})
		buttons = buttons[n:]
	}
	return rows
}

// panelItems is every ticket type, then every form, in this server.
func panelItems(guildID string) ([]panelItem, error) {
	var items []panelItem
	if tickets != nil {
		types, err := tickets.ListTypes(guildID)
		if err != nil {
			return nil, err
		}
		for _, tt := range types {
			items = append(items, ticketItem(tt))
		}
	}
	if appForms != nil {
		forms, err := appForms.ListForms(guildID)
		if err != nil {
			return nil, err
		}
		for _, f := range forms {
			items = append(items, formItem(f))
		}
	}
	return items, nil
}

func handleMyTicketsButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	if customID != "tickets:mine" {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "This only works inside a server.")
		return
	}
	userID := interactionUser(i).ID
	var lines []string
	if tickets != nil {
		types, err := tickets.ListTypes(i.GuildID)
		if err != nil {
			respondEphemeral(s, i, errorText("Mine", err))
			return
		}
		for _, tt := range types {
			open, err := tickets.OpenTicketsOf(tt.ID, userID)
			if err != nil {
				respondEphemeral(s, i, errorText("Mine", err))
				return
			}
			for _, t := range open {
				lines = append(lines, fmt.Sprintf("🎫 **%s** ticket #%04d: <#%s>, opened <t:%d:R>", tt.Name, t.Number, t.ChannelID, t.OpenedAt.Unix()))
			}
		}
	}
	if appForms != nil {
		forms, err := appForms.ListForms(i.GuildID)
		if err != nil {
			respondEphemeral(s, i, errorText("Mine", err))
			return
		}
		for _, f := range forms {
			subs, err := appForms.SubmissionsOf(f.ID, userID)
			if err != nil {
				respondEphemeral(s, i, errorText("Mine", err))
				return
			}
			if len(subs) > 0 {
				latest := subs[0]
				line := fmt.Sprintf("📝 **%s** #%d, <t:%d:R>: %s", f.Name, latest.ID, latest.SubmittedAt.Unix(), subStatusWords[latest.Status])
				if len(subs) > 1 {
					line += fmt.Sprintf(" (and %d earlier)", len(subs)-1)
				}
				lines = append(lines, line)
			}
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "You have no open tickets or submissions here. Press a button above to start."
	}
	respondEphemeral(s, i, truncateRunes(text, 1900))
}

// ============================================================================
// THE MENU
// ============================================================================

type panelDraft struct {
	userID, guildID, channelID, title, text string
	listed                                  []panelItem // offered, in order
	chosen                                  []string    // panelItem keys
	expires                                 time.Time
}

var (
	panelDraftsMu sync.Mutex
	panelDrafts   = map[string]*panelDraft{}
)

func (p *panelDraft) words(note string) string {
	names := map[string]string{}
	for _, it := range p.listed {
		names[it.key()] = it.Name
	}
	var picked []string
	for _, k := range p.chosen {
		picked = append(picked, "**"+names[k]+"**")
	}
	list := strings.Join(picked, ", ")
	if list == "" {
		list = "none yet"
	}
	text := fmt.Sprintf("A panel called **%s** for <#%s>. Pick its ticket types and forms (their buttons go in the order listed), then press **Post the panel**.\nPicked: %s",
		p.title, p.channelID, list)
	if note != "" {
		text = note + "\n\n" + text
	}
	return text
}

func (p *panelDraft) components(token string) []discordgo.MessageComponent {
	shown := p.listed
	if len(shown) > 25 {
		shown = shown[:25]
	}
	chosen := map[string]bool{}
	for _, k := range p.chosen {
		chosen[k] = true
	}
	options := make([]discordgo.SelectMenuOption, len(shown))
	for n, it := range shown {
		desc := "🎫 ticket type"
		if it.Kind == "f" {
			desc = "📝 form"
		}
		if !it.Open {
			desc += " · closed for now"
		}
		options[n] = discordgo.SelectMenuOption{Label: truncateRunes(it.Name, 100), Value: it.key(), Description: desc, Default: chosen[it.key()]}
		if e, err := parseButtonEmoji(it.Emoji); err == nil && e != nil {
			options[n].Emoji = e
		}
	}
	least := 1
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
			MenuType: discordgo.StringSelectMenu, CustomID: "tktpanel:pick:" + token, Placeholder: "Pick ticket types and forms for the panel…",
			MinValues: &least, MaxValues: min(len(shown), panelMaxButtons), Options: options,
		}}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{
			Label: "Post the panel", Style: discordgo.SuccessButton, CustomID: "tktpanel:post:" + token, Disabled: len(p.chosen) == 0,
		}}},
	}
}

func ticketPanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	items, err := panelItems(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("/ticket panel", err))
		return
	}
	if len(items) == 0 {
		respondEphemeral(s, i, "Make a ticket type (`/tickettype create`) or a form (`/form create`) first.")
		return
	}
	title, _ := optString(opts, "title")
	text, _ := optString(opts, "text")
	channelID := i.ChannelID
	if ch := optChannel(i, opts, "channel"); ch != nil {
		channelID = ch.ID
	}
	p := &panelDraft{
		userID: interactionUser(i).ID, guildID: i.GuildID, channelID: channelID,
		title: strings.TrimSpace(title), text: panelText(text), listed: items, expires: time.Now().Add(formDraftTTL),
	}
	token := newToken()
	panelDraftsMu.Lock()
	for t, old := range panelDrafts {
		if time.Now().After(old.expires) {
			delete(panelDrafts, t)
		}
	}
	panelDrafts[token] = p
	panelDraftsMu.Unlock()
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: p.words(""), Components: p.components(token), Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /ticket panel: %v", err)
	}
}

func handlePanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	if len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	token := parts[2]
	panelDraftsMu.Lock()
	p, ok := panelDrafts[token]
	panelDraftsMu.Unlock()
	if !ok || time.Now().After(p.expires) || p.userID != interactionUser(i).ID {
		updateMessage(s, i, "This menu has expired. Run `/ticket panel` again.", nil)
		return
	}
	switch parts[1] {
	case "pick":
		picked := map[string]bool{}
		for _, v := range i.MessageComponentData().Values {
			picked[v] = true
		}
		p.chosen = nil
		for _, it := range p.listed {
			if picked[it.key()] {
				p.chosen = append(p.chosen, it.key())
			}
		}
		updateMessage(s, i, p.words(""), p.components(token))
	case "post":
		said, err := postPanel(s, p)
		if err != nil {
			updateMessage(s, i, p.words("⚠️ "+errorText("Posting a panel", err)), p.components(token))
			return
		}
		panelDraftsMu.Lock()
		delete(panelDrafts, token)
		panelDraftsMu.Unlock()
		updateMessage(s, i, said, nil)
	default:
		respondEphemeral(s, i, "This button no longer does anything.")
	}
}

func postPanel(s *discordgo.Session, p *panelDraft) (string, error) {
	current, err := panelItems(p.guildID)
	if err != nil {
		return "", err
	}
	byKey := map[string]panelItem{}
	for _, it := range current {
		byKey[it.key()] = it
	}
	var chosen []panelItem
	for _, k := range p.chosen {
		if it, ok := byKey[k]; ok {
			chosen = append(chosen, it)
		}
	}
	if len(chosen) == 0 {
		return "", userErrorf("Pick at least one ticket type or form for the panel.")
	}
	if len(chosen) > panelMaxButtons {
		return "", userErrorf("A panel can hold %d buttons (Discord allows 25 on a message).", panelMaxButtons)
	}
	for _, it := range chosen {
		if it.Kind != "f" {
			continue
		}
		if qs, err := appForms.Questions(it.ID); err != nil {
			return "", err
		} else if len(qs) == 0 {
			return "", userErrorf("The form **%s** has no questions yet. Add some with `/form question add`.", it.Name)
		}
	}
	if err := checkPanelChannel(s, p.channelID); err != nil {
		return "", err
	}
	text := p.text
	if text == "" {
		text = "Press a button below."
	}
	_, err = s.ChannelMessageSendComplex(p.channelID, &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{{Title: p.title, Description: text, Color: ticketColor}},
		Components:      panelButtons(chosen, true),
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		return "", userErrorf("I couldn't post in <#%s>. Check my permissions there.", p.channelID)
	}
	var closed []string
	for _, it := range chosen {
		if !it.Open {
			closed = append(closed, "**"+it.Name+"**")
		}
	}
	note := ""
	if len(closed) > 0 {
		note = " Closed for now: " + strings.Join(closed, ", ") + "."
	}
	return fmt.Sprintf("✅ Posted a panel with %d button(s) in <#%s>.%s", len(chosen), p.channelID, note), nil
}
