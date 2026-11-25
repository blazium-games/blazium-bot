package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// Buttons, menus, pop-ups and autocomplete.
//
// Slash commands go through SlashCommandManager.HandleInteraction. Everything else a
// member does (clicking a button, submitting a pop-up, typing into an option with
// suggestions) is routed here. Buttons and pop-ups are routed by the part of their
// custom ID before the first ':' ("signup:12:345" goes to the "signup" handler), so a
// handler rebuilds everything it needs from the ID and nothing is kept in memory:
// posted buttons keep working after a restart. Never change an ID format that is
// already posted on a message.

type interactionRoute func(s *discordgo.Session, i *discordgo.InteractionCreate, customID string)

var (
	routesMu           sync.RWMutex
	componentRoutes    = map[string]interactionRoute{}
	modalRoutes        = map[string]interactionRoute{}
	autocompleteRoutes = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){}
)

// registerComponentRoute routes button and select-menu clicks whose custom ID starts with "prefix:".
func registerComponentRoute(prefix string, route interactionRoute) {
	routesMu.Lock()
	defer routesMu.Unlock()
	componentRoutes[prefix] = route
}

// registerModalRoute routes pop-up submits whose custom ID starts with "prefix:".
func registerModalRoute(prefix string, route interactionRoute) {
	routesMu.Lock()
	defer routesMu.Unlock()
	modalRoutes[prefix] = route
}

// registerAutocompleteRoute answers option suggestions for a slash command.
func registerAutocompleteRoute(command string, route func(s *discordgo.Session, i *discordgo.InteractionCreate)) {
	routesMu.Lock()
	defer routesMu.Unlock()
	autocompleteRoutes[command] = route
}

func routePrefix(customID string) string {
	prefix, _, _ := strings.Cut(customID, ":")
	return prefix
}

// dispatchNonCommandInteraction handles every interaction that isn't a slash command.
// It reports whether the interaction was one of those.
func dispatchNonCommandInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	switch i.Type {
	case discordgo.InteractionMessageComponent:
		customID := i.MessageComponentData().CustomID
		routesMu.RLock()
		route := componentRoutes[routePrefix(customID)]
		routesMu.RUnlock()
		if route == nil {
			appLogger.Warnf("No handler for button %q", customID)
			respondEphemeral(s, i, "This button no longer does anything.")
			return true
		}
		runRoute(s, i, "button "+customID, func() { route(s, i, customID) })
		return true
	case discordgo.InteractionModalSubmit:
		customID := i.ModalSubmitData().CustomID
		routesMu.RLock()
		route := modalRoutes[routePrefix(customID)]
		routesMu.RUnlock()
		if route == nil {
			appLogger.Warnf("No handler for pop-up %q", customID)
			respondEphemeral(s, i, "That form has expired. Please try again.")
			return true
		}
		runRoute(s, i, "pop-up "+customID, func() { route(s, i, customID) })
		return true
	case discordgo.InteractionApplicationCommandAutocomplete:
		name := i.ApplicationCommandData().Name
		routesMu.RLock()
		route := autocompleteRoutes[name]
		routesMu.RUnlock()
		if route == nil {
			respondChoices(s, i, nil)
			return true
		}
		runRoute(s, i, "autocomplete /"+name, func() { route(s, i) })
		return true
	}
	return false
}

// runRoute runs a handler and turns a panic into a log line and a polite reply. Unlike
// slash commands, a failing button is never switched off: the next click tries again.
func runRoute(s *discordgo.Session, i *discordgo.InteractionCreate, what string, work func()) {
	defer func() {
		if r := recover(); r != nil {
			appLogger.Errorf("%s panicked: %v\n%s", what, r, debug.Stack())
			replyEphemeral(s, i, "Something went wrong. Please try again, or let an admin know.")
		}
	}()
	work()
}

// ============================================================================
// REPLIES
// ============================================================================

// userError is a mistake to show the member as it is, not a bug to log.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// errorText is what to tell the member about err.
func errorText(what string, err error) string {
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	appLogger.Errorf("%s failed: %v", what, err)
	return "Something went wrong. Please try again, or let an admin know."
}

// respondEphemeral answers an interaction that hasn't been answered yet with a message only the member sees.
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         content,
			Flags:           discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not answer an interaction: %v", err)
	}
}

// deferEphemeral shows "thinking…" to the member while slow work runs. Finish with editResponse.
func deferEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
}

// editResponse replaces the deferred "thinking…" message with content.
func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:         &content,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		appLogger.Warnf("Could not edit an interaction response: %v", err)
	}
}

// replyEphemeral answers whether or not the interaction was already answered or deferred.
func replyEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         content,
			Flags:           discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err == nil {
		return
	}
	_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content:         content,
		Flags:           discordgo.MessageFlagsEphemeral,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		appLogger.Warnf("Could not reply to an interaction: %v", err)
	}
}

// respondChoices answers an autocomplete request. Discord shows at most 25.
func respondChoices(s *discordgo.Session, i *discordgo.InteractionCreate, choices []*discordgo.ApplicationCommandOptionChoice) {
	if len(choices) > 25 {
		choices = choices[:25]
	}
	if choices == nil {
		choices = []*discordgo.ApplicationCommandOptionChoice{}
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
	if err != nil {
		appLogger.Debugf("Could not answer autocomplete: %v", err)
	}
}

// isNotFound reports whether a Discord API error is a 404 (a deleted message, channel or role).
func isNotFound(err error) bool {
	var rest *discordgo.RESTError
	return errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == http.StatusNotFound
}

// ============================================================================
// OPTIONS
// ============================================================================

// commandOptions returns the subcommand used (or "") and its options by name.
func commandOptions(i *discordgo.InteractionCreate) (string, map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	opts := i.ApplicationCommandData().Options
	sub := ""
	if len(opts) > 0 && opts[0].Type == discordgo.ApplicationCommandOptionSubCommand {
		sub = opts[0].Name
		opts = opts[0].Options
	}
	byName := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(opts))
	for _, o := range opts {
		byName[o.Name] = o
	}
	return sub, byName
}

// focusedOption is the option the member is typing in, for autocomplete.
func focusedOption(i *discordgo.InteractionCreate) *discordgo.ApplicationCommandInteractionDataOption {
	_, opts := commandOptions(i)
	for _, o := range opts {
		if o.Focused {
			return o
		}
	}
	return nil
}

func optString(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) (string, bool) {
	if o, ok := opts[name]; ok {
		return o.StringValue(), true
	}
	return "", false
}

func optInt(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) (int64, bool) {
	if o, ok := opts[name]; ok {
		return o.IntValue(), true
	}
	return 0, false
}

func optBool(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) (bool, bool) {
	if o, ok := opts[name]; ok {
		return o.BoolValue(), true
	}
	return false, false
}

// optRole is a role option, taken from the data Discord sent with the command (no API call).
func optRole(i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) *discordgo.Role {
	o, ok := opts[name]
	if !ok {
		return nil
	}
	id, _ := o.Value.(string)
	if r := i.ApplicationCommandData().Resolved; r != nil && r.Roles != nil {
		if role, ok := r.Roles[id]; ok {
			return role
		}
	}
	return &discordgo.Role{ID: id}
}

// optChannel is a channel option, taken from the data Discord sent with the command.
func optChannel(i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) *discordgo.Channel {
	o, ok := opts[name]
	if !ok {
		return nil
	}
	id, _ := o.Value.(string)
	if r := i.ApplicationCommandData().Resolved; r != nil && r.Channels != nil {
		if ch, ok := r.Channels[id]; ok {
			return ch
		}
	}
	return &discordgo.Channel{ID: id}
}

// ============================================================================
// PERMISSIONS
// ============================================================================

// permManageGuild is the default permission for staff tools that change the server (panels, forms).
var permManageGuild = int64(discordgo.PermissionManageGuild)

// canManageServer: Manage Server or Administrator, or the STAFF_ROLE_ID role. Checked on
// every use, because server admins can open a command to anyone in Server Settings.
func canManageServer(i *discordgo.InteractionCreate) bool {
	if i.Member == nil {
		return false
	}
	if i.Member.Permissions&(discordgo.PermissionManageGuild|discordgo.PermissionAdministrator) != 0 {
		return true
	}
	staffRoleID := os.Getenv("STAFF_ROLE_ID")
	if staffRoleID == "" {
		return false
	}
	for _, roleID := range i.Member.Roles {
		if roleID == staffRoleID {
			return true
		}
	}
	return false
}
