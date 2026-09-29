// Package scalesetadapter binds the actions/scaleset client to the dispatcher
// interfaces.
package scalesetadapter

import (
	"context"
	"fmt"

	"github.com/actions/scaleset"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/dispatcher"
)

// AppConfig carries GitHub App authentication settings.
type AppConfig struct {
	GitHubConfigURL string
	ClientID        string
	InstallationID  int64
	PrivateKey      string
}

// Client wraps one authenticated scale-set client.
type Client struct {
	client *scaleset.Client
}

// NewClient authenticates with a GitHub App installation.
func NewClient(cfg AppConfig) (*Client, error) {
	c, err := scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
		GitHubConfigURL: cfg.GitHubConfigURL,
		GitHubAppAuth: scaleset.GitHubAppAuth{
			ClientID:       cfg.ClientID,
			InstallationID: cfg.InstallationID,
			PrivateKey:     cfg.PrivateKey,
		},
		SystemInfo: scaleset.SystemInfo{
			System:  "peasant-runner-dispatcher",
			Version: "spike",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("github app client: %w", err)
	}
	return &Client{client: c}, nil
}

// EnsureScaleSet returns the id of the named scale set in the runner group,
// creating the scale set when it does not exist yet.
func (c *Client) EnsureScaleSet(ctx context.Context, name, runnerGroup string, labels []string) (int, error) {
	group, err := c.client.GetRunnerGroupByName(ctx, runnerGroup)
	if err != nil {
		return 0, fmt.Errorf("runner group %q: %w", runnerGroup, err)
	}
	existing, err := c.client.GetRunnerScaleSet(ctx, group.ID, name)
	if err != nil {
		return 0, fmt.Errorf("look up scale set %q: %w", name, err)
	}
	if existing != nil {
		return existing.ID, nil
	}
	created, err := c.client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          name,
		RunnerGroupID: group.ID,
		Labels:        toLabels(labels),
	})
	if err != nil {
		return 0, fmt.Errorf("create scale set %q: %w", name, err)
	}
	return created.ID, nil
}

func toLabels(labels []string) []scaleset.Label {
	out := make([]scaleset.Label, 0, len(labels))
	for _, label := range labels {
		out = append(out, scaleset.Label{Name: label})
	}
	return out
}

// Session opens the message session for one scale set.
func (c *Client) Session(ctx context.Context, scaleSetID int, owner string) (*MessageSession, error) {
	sess, err := c.client.MessageSessionClient(ctx, scaleSetID, owner)
	if err != nil {
		return nil, fmt.Errorf("message session: %w", err)
	}
	return &MessageSession{sess: sess}, nil
}

// MessageSession adapts scaleset.MessageSessionClient to dispatcher.Session.
type MessageSession struct {
	sess *scaleset.MessageSessionClient
}

// Next implements dispatcher.Session.
func (m *MessageSession) Next(ctx context.Context, lastMessageID, maxCapacity int) (*dispatcher.Message, error) {
	msg, err := m.sess.GetMessage(ctx, lastMessageID, maxCapacity)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, nil
	}
	out := &dispatcher.Message{ID: msg.MessageID}
	if msg.Statistics != nil {
		out.Statistics = dispatcher.Statistics{
			AssignedJobs: msg.Statistics.TotalAssignedJobs,
			RunningJobs:  msg.Statistics.TotalRunningJobs,
		}
	}
	return out, nil
}

// Ack implements dispatcher.Session.
func (m *MessageSession) Ack(ctx context.Context, messageID int) error {
	return m.sess.DeleteMessage(ctx, messageID)
}

// Close releases the session.
func (m *MessageSession) Close(ctx context.Context) error {
	return m.sess.Close(ctx)
}

// JITMinter mints one-shot runner registrations for one scale set.
type JITMinter struct {
	client     *scaleset.Client
	scaleSetID int
	namePrefix string
	workFolder string
}

// JITMinter returns a minter that names runners with the given prefix.
func (c *Client) JITMinter(scaleSetID int, namePrefix, workFolder string) *JITMinter {
	return &JITMinter{client: c.client, scaleSetID: scaleSetID, namePrefix: namePrefix, workFolder: workFolder}
}

// Mint implements dispatcher.JITSource.
func (m *JITMinter) Mint(ctx context.Context) (string, error) {
	cfg, err := m.client.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       m.namePrefix,
		WorkFolder: m.workFolder,
	}, m.scaleSetID)
	if err != nil {
		return "", fmt.Errorf("generate jit config: %w", err)
	}
	return cfg.EncodedJITConfig, nil
}
