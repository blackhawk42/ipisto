package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	kongtoml "github.com/alecthomas/kong-toml"
	"github.com/bwmarrin/discordgo"
)

// getPublicIP gets the public IP address of the current machine.
//
// ipURL should be an URL that, when sent a GET request, will respond with a
// simple text body containing the IP.
//
// If client is nil, http.DefaultClient will be used.
func getPublicIP(client *http.Client, ipURL string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	r, err := client.Get(ipURL)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	ipStr, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}

	ip, err := netip.ParseAddr(string(ipStr))
	if err != nil {
		return "", err
	}

	return ip.String(), nil
}

// newCommandHandler creates a new handler for our command.
//
// If client is nil, http.DefaultClient will be used.
func newCommandHandler(slashCommandName string, IPURL string, client *http.Client) func(s *discordgo.Session, i *discordgo.InteractionCreate) {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.ApplicationCommandData().Name != slashCommandName {
			return
		}

		if i.User != nil {
			slog.Info("command activated in a DM", "dmuser", i.User.String(), "dmuserid", i.User.ID, "interactionid", i.Interaction.ID)
		} else if i.Member != nil {
			slog.Info("command activated in a guild", "guilduser", i.Member.User.String(), "guilduserid", i.Member.User.ID, "guildid", i.GuildID, "interactionid", i.Interaction.ID)
		} else {
			slog.Info("command activated, by unkown means...", "interactionid", i.Interaction.ID)
		}

		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		})
		if err != nil {
			slog.Error("error while responding to interaction", "error", err, "interactionid", i.Interaction.ID)
			return
		}
		slog.Info("interaction responded sucessfully", "interactionid", i.Interaction.ID)

		ip, err := getPublicIP(client, IPURL)
		if err != nil {
			slog.Error("error while getting public IP", "error", err, "interactionid", i.Interaction.ID)
			return
		}
		slog.Info("IP address retrieved", "publicip", ip, "interaction", i.Interaction.ID)

		_, err = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: ip,
		})
		if err != nil {
			slog.Error("error while sending followup message", "error", err, "interactionid", i.Interaction.ID)
			return
		}

		slog.Info("command responded sucessfully, exiting handler", "interactionid", i.Interaction.ID)
	}
}

type CLI struct {
	BotToken         string          `placeholder:"TOKEN" env:"${tokenEnv}" help:"Discord bot token. It MUST be precent in some way to proceed. May also use config file or the corresponding environment variable."`
	Config           kong.ConfigFlag `placeholder:"CONFIG-FILE" help:"Optional TOML configuration file."`
	SlashCommandName string          `default:"publicip" help:"Name of the Discord command to register."`
	IPURL            *url.URL        `default:"https://ipinfo.io/ip" help:"URL to get public IP from. It should be an URL that, when given a GET request, will respond with the public IP of the request as simple text in its body."`
}

func main() {
	// Configuration
	var args CLI
	kongCtx := kong.Parse(
		&args,
		kong.Description("Discord bot to get the public IP of the machine running it."),
		kong.Configuration(kongtoml.Loader),
		kong.Vars{
			"tokenEnv": "IPISTO_BOT_TOKEN",
		},
	)

	var err error
	if args.BotToken == "" {
		kongCtx.Fatalf("bot token is empty")
	}

	// Setting logfmt as default format for log messages
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Start of bot proper
	slog.Info("bot started", "config", args.Config)

	slog.Info("creating Discord session")
	discordSession, err := discordgo.New("Bot " + args.BotToken)
	if err != nil {
		slog.Error("error while creating Discord session", "error", err)
		kongCtx.Exit(1)
	}

	discordSession.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		slog.Info("logged in", "username", s.State.User.Username, "discriminator", s.State.User.Discriminator, "id", s.State.User.ID)
	})
	discordSession.AddHandler(newCommandHandler(args.SlashCommandName, args.IPURL.String(), nil))

	slog.Info("openning Websocket conection")
	err = discordSession.Open()
	if err != nil {
		slog.Error("error while opening websocket connection", "error", err)
		kongCtx.Exit(1)
	}
	defer discordSession.Close()

	slog.Info("registering command", "slashcommandname", args.SlashCommandName)
	registeredCommand, err := discordSession.ApplicationCommandCreate(
		discordSession.State.User.ID,
		"",
		&discordgo.ApplicationCommand{
			Name:        args.SlashCommandName,
			Description: "Get public IP of this bot's server",
		},
	)
	if err != nil {
		slog.Error("error while registering command", "error", err)
		discordSession.Close()
		kongCtx.Exit(1)
	}
	slog.Info("command registered", "commandid", registeredCommand.ID)

	stop := make(chan os.Signal, 2)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	slog.Info("signal to stop received")

	slog.Info("deleting command")
	err = discordSession.ApplicationCommandDelete(discordSession.State.User.ID, "", registeredCommand.ID)
	if err != nil {
		slog.Error("error while deleting command", "error", err)
		discordSession.Close()
		kongCtx.Exit(1)
	}

	slog.Info("exiting")
}
