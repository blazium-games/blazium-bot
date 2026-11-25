This is a Discord bot for Blazium
## Running a server

Besides sign-up panels, tickets and forms, the bot has the everyday tools for running a community server:

- **Moderation**: `/warn`, `/timeout`, `/untimeout`, `/kick`, `/ban`, `/unban`. Each one becomes a numbered case (`/cases`, `/case show|reason|delete`), is posted to the mod log and is sent to the member by DM. Members level with or above the moderator (or the bot) can't be acted on.
- **Channel tools**: `/purge` (with member, text and bot filters), `/slowmode`, `/lock`, `/unlock`.
- **Settings** (`/settings`, Manage Server): mod log, member log (joins and leaves) and message log (deleted and edited messages) channels, welcome and goodbye messages with `{user}`, `{username}`, `{server}` and `{count}`, and auto-roles for new members.
- **Announcements** (`/announce send|edit`, Manage Server): write a styled embed in a pop-up (title, message, color, image, footer), optionally ping @everyone, @here or a role, publish it to following servers, and edit it later from its message link.
- **Reminders** (`/remind me|list|cancel`, everyone): "remind me in 2h about…" by DM or in the channel. Stored in Postgres, so they survive restarts.
- **Info**: `/serverinfo`, `/userinfo`, `/avatar`, `/roleinfo`, and `/help`, which lists only the commands the member can use.

Settings and case history are stored in Postgres (`POSTGRES_CON_STRING`).

### Privileged intents

Some of these need privileged gateway intents. Discord refuses the bot's whole connection if it asks for one that isn't switched on, so the bot only asks when told to:

| Variable | Turn on in the Developer Portal (Bot > Privileged Gateway Intents) | Needed for |
| --- | --- | --- |
| `DISCORD_MEMBERS_INTENT=true` | Server Members Intent | welcome and goodbye messages, auto-roles, member log |
| `DISCORD_MESSAGE_CONTENT_INTENT=true` | Message Content Intent | the text of deleted and edited messages in the message log |
