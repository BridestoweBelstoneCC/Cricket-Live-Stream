package app

// Alerts on the club's Discord channel: setup's [Alerts] discord_webhook. Read at each
// alert, so a webhook added in Settings works from the next one without a restart.

import (
	"fmt"
	"strings"

	"cricketstream/internal/discord"
	"cricketstream/internal/ini"
	"cricketstream/internal/setup"
)

// discordPost is discord.Post (a test seam: tests never reach Discord).
var discordPost = discord.Post

// discordMessage is a notice as the channel reads it: which club (a channel may hear
// from more than one laptop, or a league's clubs), what, and what to do.
func discordMessage(club string, n notice) string {
	mark := "✅"
	if n.Warn {
		mark = "⚠️"
	}
	head := "**" + n.Title + "**"
	if club = strings.TrimSpace(club); club != "" {
		head = club + ": " + head
	}
	return mark + " " + head + "\n" + n.Text
}

func sendDiscord(n notice) {
	f, err := ini.Load(setup.ConfigFile())
	if err != nil {
		return
	}
	hook := strings.TrimSpace(f.Get("Alerts", "discord_webhook", ""))
	if hook == "" {
		return
	}
	if err := discordPost(hook, discordMessage(f.Get("Club", "name", ""), n)); err != nil {
		// The error never holds the link (it's a password): discord.Post takes it out.
		fmt.Println("  ⚠ Couldn't send that alert to Discord: " + err.Error())
	}
}
