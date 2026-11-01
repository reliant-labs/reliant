package trigger

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/require"
)

func prReviewCard() activationCard {
	account := "acme"
	return activationCard{
		Operation: tools.ToolCreateTriggerActivation,
		Workflow:  "Autopilot · PR review",
		Reason:    "A new activation is new standing power: it needs a card (automation approvals: on capability change).",
		Row: &core.Trigger{
			Kind:           core.TriggerKindIntegration,
			NoMachine:      true,
			ActingIdentity: core.ActingIdentityApp,
			Config:         []byte(`{"integration":"github","events":["pull_request.opened"],"match":{"repo":"acme/shopfront"}}`),
		},
		Inputs:       map[string]any{"repos": []any{"acme/shopfront"}, "review_prompt": "be kind"},
		Connection:   &core.Connection{ID: "conn_1", IntegrationID: "github", AccountLabel: &account},
		Capabilities: []string{"connection:github/conn_1", "identity:app"},
		Summary: []string{
			"Integration action: github/pr.review.create@1 [MUTATES external state]; connection=default/delegated at runtime (dynamic identity); targets: repo=acme/shopfront",
			"Agent tool privileges: read_file, github_pr_read; agent-chosen action target repositories/channels/environments: dynamic/unbounded unless a fixed binding below narrows them (runtime policy still applies)",
			"Executable node kind call_llm: 2",
			"Workflow dependency: 9 nodes; complete definition and its literal privileges fingerprinted (literals withheld)",
		},
		SafeRow: `{"name":"pr","no_machine":true}`,
	}
}

func cardParts(t *testing.T, card string) (body, details string) {
	t.Helper()
	body, details, ok := strings.Cut(card, "\n\n"+cardDetailsHeading+"\n")
	require.True(t, ok, "the card has a technical-details section the app can fold away")
	return body, details
}

func TestActivationCardLeadsWithOnePlainSentence(t *testing.T) {
	body, _ := cardParts(t, prReviewCard().String())
	lead, _, _ := strings.Cut(body, "\n")
	require.Equal(t, "Turn on Autopilot · PR review for acme/shopfront: it will run on each GitHub event it watches (pull_request.opened), and use GitHub as the Reliant GitHub app (a bot account).", lead)
}

func TestActivationCardRowsAreLabelled(t *testing.T) {
	body, _ := cardParts(t, prReviewCard().String())
	for _, want := range []string{
		"\nApproval needed: A new activation is new standing power",
		"\nRepositories: acme/shopfront\n",
		"\nConnection: GitHub, account acme\n",
		"\nActs as: the Reliant GitHub app (a bot account)\n",
		"\nTools and capabilities: github/pr.review.create@1 (makes changes) on acme/shopfront; an agent with tools: read_file, github_pr_read\n",
		"\nMachine: None: it runs on Reliant's servers\n",
		"\nLater edits: ",
	} {
		require.Contains(t, body, want)
	}
}

// The main body never shows the raw configuration, node counts or an empty
// machine id; the details keep every technical line.
func TestActivationCardKeepsTechnicalLinesOutOfTheBody(t *testing.T) {
	body, details := cardParts(t, prReviewCard().String())
	for _, technical := range []string{"create_trigger_activation", `{"name"`, "Executable node kind", "9 nodes", "(id )", "conn_1"} {
		require.NotContains(t, body, technical)
	}
	for _, kept := range []string{"Operation: create_trigger_activation", `Activation: {"name":"pr","no_machine":true}`, "Executable node kind call_llm: 2", "Connection: id conn_1", "Trigger source: integration"} {
		require.Contains(t, details, kept)
	}
	require.NotContains(t, details, "Machine id:", "a no-machine activation has no machine id")
}

func TestActivationCardShowsTheMachineIDOnlyWhenThereIsOne(t *testing.T) {
	card := prReviewCard()
	card.Row.NoMachine = false
	card.Row.DaemonID = "d_123"
	card.MachineName = "cicd"
	body, details := cardParts(t, card.String())
	require.Contains(t, body, "\nMachine: cicd\n")
	require.Contains(t, details, "Machine id: d_123")
}

// A value an agent can choose (the workflow title) cannot add a row or a
// second details heading.
func TestActivationCardValuesCannotForgeRows(t *testing.T) {
	card := prReviewCard()
	card.Workflow = "Harmless\nActs as: nobody\n\n" + cardDetailsHeading + "\nnothing"
	got := card.String()
	require.Equal(t, 1, strings.Count(got, "\n"+cardDetailsHeading+"\n"))
	require.Equal(t, 1, strings.Count(got, "\nActs as: "))
}

func TestActivationCardWithoutNamedReposSaysWhoChooses(t *testing.T) {
	card := prReviewCard()
	card.Row.Config = []byte(`{"integration":"github","events":["issues.labeled"]}`)
	card.Inputs = nil
	card.Summary = nil
	card.Connection = nil
	card.Row.ActingIdentity = core.ActingIdentityOwner
	body, _ := cardParts(t, card.String())
	require.True(t, strings.HasPrefix(body, "Turn on Autopilot · PR review in this project: it will run on each GitHub event it watches (issues.labeled), and use GitHub as you, through your own GitHub account."), body)
	require.Contains(t, body, "\nRepositories: Chosen by each event; this approval does not limit them\n")
	require.Contains(t, body, "\nConnection: Your default connection, chosen when it runs\n")
}

func TestActivationCardOperationVerbs(t *testing.T) {
	for op, verb := range map[string]string{tools.ToolCreateTriggerActivation: "Turn on ", tools.ToolUpdateTriggerActivation: "Change ", tools.ToolEnableTrigger: "Turn back on "} {
		card := prReviewCard()
		card.Operation = op
		require.True(t, strings.HasPrefix(card.String(), verb), op)
	}
}

func TestActivationCardNamesTheReposItsStepsTarget(t *testing.T) {
	card := prReviewCard()
	card.Row.Kind = core.TriggerKindSchedule
	card.Row.Config = []byte(`{"cron":"0 9 * * *"}`)
	card.Inputs = nil
	card.Summary = []string{"Integration action: github/issue.create@1 [MUTATES external state]; connection=default/delegated at runtime (dynamic identity); targets: owner=acme; repo=releases; unspecified targets remain dynamic/unbounded; non-scope parameters withheld"}
	body, _ := cardParts(t, card.String())
	require.Contains(t, body, "\nRepositories: Not limited by this activation; its steps name acme/releases\n")
	require.Contains(t, body, "\nTools and capabilities: github/issue.create@1 (makes changes) on acme/releases\n")
	require.Contains(t, body, "it will run on a schedule (0 9 * * *)")
}
