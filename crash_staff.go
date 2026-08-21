package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type crashClient struct {
	base   string
	token  string
	client *http.Client
}

func newCrashClient() *crashClient {
	base := strings.TrimSpace(os.Getenv("CRASH_API"))
	if base == "" {
		base = "https://crash.blazium.app"
	}
	return &crashClient{
		base:  strings.TrimRight(base, "/"),
		token: strings.TrimSpace(os.Getenv("CRASH_STAFF_TOKEN")),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *crashClient) do(method, path string) ([]byte, int, error) {
	if c.token == "" {
		return nil, 0, fmt.Errorf("CRASH_STAFF_TOKEN is not set")
	}
	req, err := http.NewRequest(method, c.base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Bot-Secret", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, fmt.Errorf("crash api status %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func handleCrashCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		crashFollowup(s, i, "Choose a subcommand: list, show, download, or analyze.")
		return
	}
	sub := options[0].Name
	client := newCrashClient()
	switch sub {
	case "list":
		appID := GetStringOption(i, "app_id")
		path := "/v1/staff/reports"
		if appID != "" {
			path += "?app_id=" + url.QueryEscape(appID)
		}
		body, _, err := client.do(http.MethodGet, path)
		if err != nil {
			crashFollowup(s, i, "Could not list crash reports.")
			return
		}
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			crashFollowup(s, i, "Could not read crash report list.")
			return
		}
		if len(rows) == 0 {
			crashFollowup(s, i, "No crash reports found.")
			return
		}
		var b strings.Builder
		limit := len(rows)
		if limit > 15 {
			limit = 15
		}
		for idx := 0; idx < limit; idx++ {
			row := rows[idx]
			fmt.Fprintf(&b, "`%s` %s / %s dump=%v anon=%v\n",
				strAny(row["id"]),
				strAny(row["app_id"]),
				strAny(row["build_id"]),
				row["has_dump"],
				row["anonymous"],
			)
		}
		if len(rows) > limit {
			fmt.Fprintf(&b, "… %d more\n", len(rows)-limit)
		}
		crashFollowup(s, i, strings.TrimSpace(b.String()))
	case "show":
		id := GetStringOption(i, "id")
		if id == "" {
			crashFollowup(s, i, "Report id is required.")
			return
		}
		body, _, err := client.do(http.MethodGet, "/v1/staff/reports/"+url.PathEscape(id))
		if err != nil {
			crashFollowup(s, i, "Could not load that crash report.")
			return
		}
		var row map[string]any
		if err := json.Unmarshal(body, &row); err != nil {
			crashFollowup(s, i, "Could not read that crash report.")
			return
		}
		analysis, _ := row["analysis"].(map[string]any)
		msg := fmt.Sprintf(
			"**%s**\napp: %s\nbuild: %s\nanonymous: %v\ndump/log/stack: %v/%v/%v\nmessage: %s\nreason: %s",
			strAny(row["id"]),
			strAny(row["app_id"]),
			strAny(row["build_id"]),
			row["anonymous"],
			row["has_dump"],
			row["has_log"],
			row["has_stack"],
			strAny(row["user_message"]),
			strAny(analysis["crash_reason"]),
		)
		crashFollowup(s, i, msg)
	case "download":
		id := GetStringOption(i, "id")
		kind := GetStringOption(i, "kind")
		if id == "" || kind == "" {
			crashFollowup(s, i, "Report id and kind are required.")
			return
		}
		path := "/v1/staff/reports/" + url.PathEscape(id) + "/file?kind=" + url.QueryEscape(kind)
		body, _, err := client.do(http.MethodGet, path)
		if err != nil {
			crashFollowup(s, i, "Could not create a download link.")
			return
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil || strAny(out["url"]) == "" {
			crashFollowup(s, i, "Could not create a download link.")
			return
		}
		crashFollowup(s, i, fmt.Sprintf("Presigned %s link (expires %s):\n%s", kind, strAny(out["expires_at"]), strAny(out["url"])))
	case "analyze":
		id := GetStringOption(i, "id")
		if id == "" {
			crashFollowup(s, i, "Report id is required.")
			return
		}
		_, _, err := client.do(http.MethodPost, "/v1/staff/reports/"+url.PathEscape(id)+"/analyze")
		if err != nil {
			crashFollowup(s, i, "Could not analyze that crash report.")
			return
		}
		crashFollowup(s, i, "Analysis started for `"+id+"`.")
	default:
		crashFollowup(s, i, "Unknown subcommand.")
	}
}

func crashFollowup(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
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
		appLogger.Errorf("crash followup failed: %v", err)
	}
}

func strAny(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}
