package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Panels of forms: /form panel posts one message with a button for each chosen form (a
// general ticket, a mod application…) plus 🔎 My applications. Its buttons are the forms'
// own Apply buttons (form:<id>:apply), so they behave exactly the same.
//
// /form panel first shows staff a menu to pick the forms (formpanel:pick:<token>), then
// Post the panel (formpanel:post:<token>). That menu's choices are kept in memory for 15 minutes.

const formPanelMax = 24 // one button per form, plus 🔎 My applications: Discord allows 25 on a message

type panelDraft struct {
	userID, guildID, channelID, title, text string
	listed                                  []appForm // the forms offered, in order
	chosen                                  []int64
	expires                                 time.Time
}

var (
	panelDraftsMu sync.Mutex
	panelDrafts   = map[string]*panelDraft{}
)

func (p *panelDraft) words(note string) string {
	names := map[int64]string{}
	for _, f := range p.listed {
		names[f.ID] = f.Name
	}
	var picked []string
	for _, id := range p.chosen {
		picked = append(picked, "**"+names[id]+"**")
	}
	list := strings.Join(picked, ", ")
	if list == "" {
		list = "none yet"
	}
	text := fmt.Sprintf("A panel called **%s** for <#%s>. Pick its forms (their buttons go in the order the forms are listed), then press **Post the panel**.\nPicked: %s",
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
	options := make([]discordgo.SelectMenuOption, len(shown))
	chosen := map[int64]bool{}
	for _, id := range p.chosen {
		chosen[id] = true
	}
	for n, f := range shown {
		desc := "closed for now"
		if f.Open {
			desc = "taking applications"
		}
		options[n] = discordgo.SelectMenuOption{Label: truncateRunes(f.Name, 100), Value: strconv.FormatInt(f.ID, 10), Description: desc, Default: chosen[f.ID]}
		if e, err := parseButtonEmoji(f.Emoji); err == nil && e != nil {
			options[n].Emoji = e
		}
	}
	least := 1
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
			MenuType: discordgo.StringSelectMenu, CustomID: "formpanel:pick:" + token, Placeholder: "Pick the forms for the panel…",
			MinValues: &least, MaxValues: min(len(shown), formPanelMax), Options: options,
		}}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{
			Label: "Post the panel", Style: discordgo.SuccessButton, CustomID: "formpanel:post:" + token, Disabled: len(p.chosen) == 0,
		}}},
	}
}

func formPanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("/form panel", err))
		return
	}
	if len(forms) == 0 {
		respondEphemeral(s, i, "Make some forms first, with `/form create`.")
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
		title: strings.TrimSpace(title), text: panelText(text), listed: forms, expires: time.Now().Add(formDraftTTL),
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
		appLogger.Warnf("Could not answer /form panel: %v", err)
	}
}

func handleFormPanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
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
		updateMessage(s, i, "This menu has expired. Run `/form panel` again.", nil)
		return
	}
	switch parts[1] {
	case "pick":
		picked := map[string]bool{}
		for _, v := range i.MessageComponentData().Values {
			picked[v] = true
		}
		p.chosen = nil
		for _, f := range p.listed { // in the order forms are listed
			if picked[strconv.FormatInt(f.ID, 10)] {
				p.chosen = append(p.chosen, f.ID)
			}
		}
		updateMessage(s, i, p.words(""), p.components(token))
	case "post":
		said, err := postFormPanel(s, p)
		if err != nil {
			updateMessage(s, i, p.words("⚠️ "+errorText("Posting a panel of forms", err)), p.components(token))
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

// formPanelButtons is the panel's buttons: each form's own Apply button, named after the form, then 🔎 My applications.
func formPanelButtons(chosen []appForm) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent
	for _, f := range chosen {
		b := formButton(f.ID, "apply")
		b.Label, b.Style = truncateRunes(f.Name, 80), discordgo.PrimaryButton
		if e, err := parseButtonEmoji(f.Emoji); err == nil && e != nil {
			b.Emoji = e
		}
		buttons = append(buttons, b)
	}
	buttons = append(buttons, mineButton())
	var rows []discordgo.MessageComponent
	for len(buttons) > 0 {
		n := min(5, len(buttons))
		rows = append(rows, discordgo.ActionsRow{Components: buttons[:n]})
		buttons = buttons[n:]
	}
	return rows
}

func postFormPanel(s *discordgo.Session, p *panelDraft) (string, error) {
	current, err := appForms.ListForms(p.guildID)
	if err != nil {
		return "", err
	}
	byID := map[int64]appForm{}
	for _, f := range current {
		byID[f.ID] = f
	}
	var chosen []appForm
	for _, id := range p.chosen {
		if f, ok := byID[id]; ok {
			chosen = append(chosen, f)
		}
	}
	if len(chosen) == 0 {
		return "", userErrorf("Pick at least one form for the panel.")
	}
	if len(chosen) > formPanelMax {
		return "", userErrorf("A panel can hold %d forms (Discord allows 25 buttons on a message).", formPanelMax)
	}
	for n := range chosen {
		problem, err := formReadyProblem(&chosen[n])
		if err != nil {
			return "", err
		}
		if problem != "" {
			return "", userErrorf("%s", problem)
		}
	}
	if err := checkPanelChannel(s, p.channelID); err != nil {
		return "", err
	}
	text := p.text
	if text == "" {
		text = "Press a button below. Only staff see what you send."
	}
	_, err = s.ChannelMessageSendComplex(p.channelID, &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{{Title: p.title, Description: text, Color: formColor}},
		Components:      formPanelButtons(chosen),
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		return "", userErrorf("I couldn't post in <#%s>. Check my permissions there.", p.channelID)
	}
	var closed []string
	for _, f := range chosen {
		if !f.Open {
			closed = append(closed, "**"+f.Name+"**")
		}
	}
	note := ""
	if len(closed) > 0 {
		note = " Closed for now: " + strings.Join(closed, ", ") + " (open with `/form edit open:True`)."
	}
	return fmt.Sprintf("✅ Posted a panel with %d form(s) in <#%s>.%s", len(chosen), p.channelID, note), nil
}
