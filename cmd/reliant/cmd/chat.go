// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant chat` reads your chats and moves one to another machine: what the
// app's chat header machine picker does (ChatService.SetChatDaemon). Before
// it, a script could not see which machine a chat runs on, and an agent or a
// person in a terminal could not repin one.

// chatClient is the slice of the ChatService client `reliant chat` needs;
// reliantv1connect.ChatServiceClient satisfies it.
type chatClient interface {
	ListChats(context.Context, *connect.Request[reliantv1.ListChatsRequest]) (*connect.Response[reliantv1.ListChatsResponse], error)
	GetChat(context.Context, *connect.Request[reliantv1.GetChatRequest]) (*connect.Response[reliantv1.GetChatResponse], error)
	SetChatDaemon(context.Context, *connect.Request[reliantv1.SetChatDaemonRequest]) (*connect.Response[reliantv1.SetChatDaemonResponse], error)
}

// machineLister is the slice of the DaemonRegistryService client that names
// machines.
type machineLister interface {
	ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error)
}

func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "List your chats, and see or change the machine a chat runs on",
		Long: `List your chats, show one, and move a chat to another of your machines.

A chat runs its tools on one machine: the one picked in the chat header, else
your default machine. A chat in a branch workspace stays on the machine that
holds the workspace's checkout; moving it elsewhere is refused.`,
	}
	cmd.AddCommand(newChatListCmd(), newChatShowCmd(), newChatSetMachineCmd())
	return cmd
}

// chatClients dials the ChatService and the machine registry as the CLI
// login.
var chatClients = func(cmd *cobra.Command) (chatClient, machineLister, *connection, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	return reliantv1connect.NewChatServiceClient(conn.httpClient(), conn.ServerURL), newDaemonRegistryClient(conn), conn, nil
}

func newChatListCmd() *cobra.Command {
	var (
		projectID       string
		limit           int32
		jsonOutput      bool
		includeArchived bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your chats with the machine each runs on",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			chats, machines, conn, err := chatClients(cmd)
			if err != nil {
				return err
			}
			req := &reliantv1.ListChatsRequest{ProjectId: projectID}
			if limit > 0 {
				req.Limit = proto.Int32(limit)
			}
			resp, err := chats.ListChats(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing chats: %w", err))
			}
			list := make([]*reliantv1.Chat, 0, len(resp.Msg.GetChats()))
			for _, c := range resp.Msg.GetChats() {
				if !includeArchived && c.GetState() == reliantv1.ChatState_CHAT_STATE_ARCHIVED {
					continue
				}
				list = append(list, c)
			}
			if jsonOutput {
				return writeChatsJSON(cmd.OutOrStdout(), list)
			}
			return printChats(cmd.OutOrStdout(), list, machineNames(cmd.Context(), machines))
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "List only this project's chats (a project id; default: every project)")
	cmd.Flags().Int32Var(&limit, "limit", 0, "Most chats to list (default: the server's)")
	cmd.Flags().BoolVar(&includeArchived, "archived", false, "Include archived chats")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the chats as JSON")
	return cmd
}

func newChatShowCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show <chat-id>",
		Short: "Show one chat: its project, workspace and machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			chats, machines, conn, err := chatClients(cmd)
			if err != nil {
				return err
			}
			resp, err := chats.GetChat(cmd.Context(), connect.NewRequest(&reliantv1.GetChatRequest{ChatId: strings.TrimSpace(args[0])}))
			if err != nil {
				return conn.annotate(fmt.Errorf("reading chat: %w", err))
			}
			if jsonOutput {
				return writeProtoJSON(cmd.OutOrStdout(), resp.Msg.GetChat())
			}
			return printChat(cmd.OutOrStdout(), resp.Msg.GetChat(), machineNames(cmd.Context(), machines))
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the chat as JSON")
	return cmd
}

func newChatSetMachineCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "set-machine <chat-id> [machine]",
		Short: "Run a chat's tools on another of your machines",
		Long: `Pins a chat to one of your machines, named by id or by name, as the machine
picker in the chat header does. Its next turn runs there. --clear removes the
pin, so the chat follows your default machine again.

The machine must be yours and, when the project tracks where it is installed,
have the project. A chat in a branch workspace cannot leave the machine that
holds its checkout.`,
		Example: `  reliant chat set-machine 1ea43ece-… build-box
  reliant chat set-machine 1ea43ece-… --clear`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear == (len(args) == 2) {
				return fmt.Errorf("name a machine, or pass --clear to remove the chat's pin")
			}
			chats, machines, conn, err := chatClients(cmd)
			if err != nil {
				return err
			}
			daemonID := ""
			if !clear {
				m, err := resolveRegistryMachine(cmd.Context(), machines, strings.TrimSpace(args[1]))
				if err != nil {
					return err
				}
				daemonID = m.GetDaemonId()
			}
			resp, err := chats.SetChatDaemon(cmd.Context(), connect.NewRequest(&reliantv1.SetChatDaemonRequest{
				ChatId:   strings.TrimSpace(args[0]),
				DaemonId: daemonID,
			}))
			if err != nil {
				return conn.annotate(fmt.Errorf("setting the chat's machine: %w", err))
			}
			out := cmd.OutOrStdout()
			if clear {
				_, err = fmt.Fprintf(out, "Chat %s follows your default machine.\n", resp.Msg.GetChat().GetId())
				return err
			}
			_, err = fmt.Fprintf(out, "Chat %s runs on %s from its next turn.\n", resp.Msg.GetChat().GetId(),
				machineLabel(resp.Msg.GetChat().GetActiveDaemonId(), machineNames(cmd.Context(), machines)))
			return err
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Remove the chat's machine pin, so it follows your default machine")
	return cmd
}

// machineNames maps machine ids to what Settings -> Machines calls them. A
// registry that cannot be read leaves the map empty: the chat commands then
// print ids, rather than fail over a label.
func machineNames(ctx context.Context, machines machineLister) map[string]string {
	names := map[string]string{}
	resp, err := machines.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err != nil {
		return names
	}
	for _, d := range resp.Msg.GetDaemons() {
		names[d.GetDaemonId()] = machineDisplayName(d)
	}
	return names
}

// machineLabel is a chat's machine for a person to read.
func machineLabel(daemonID string, names map[string]string) string {
	switch {
	case daemonID == "":
		return "(default machine)"
	case names[daemonID] != "":
		return fmt.Sprintf("%s (%s)", names[daemonID], daemonID)
	}
	return daemonID
}

// chatWorkspace is the branch workspace a chat runs in, or "main checkout".
func chatWorkspace(c *reliantv1.Chat) string {
	if c.GetWorktreeId() == "" {
		return "main checkout"
	}
	if b := c.GetWorktree().GetBranch(); b != "" {
		return b
	}
	return c.GetWorktreeId()
}

func chatStateName(s reliantv1.ChatState) string {
	if s == reliantv1.ChatState_CHAT_STATE_ARCHIVED {
		return "archived"
	}
	return "active"
}

func printChats(out io.Writer, chats []*reliantv1.Chat, names map[string]string) error {
	if len(chats) == 0 {
		_, err := fmt.Fprintln(out, "No chats.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tMACHINE\tWORKSPACE\tSTATE\tLAST ACTIVE")
	for _, c := range chats {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.GetId(), truncateTitle(c.GetTitle()), machineLabel(c.GetActiveDaemonId(), names),
			chatWorkspace(c), chatStateName(c.GetState()), c.GetLastActive())
	}
	return w.Flush()
}

func printChat(out io.Writer, c *reliantv1.Chat, names map[string]string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID:\t%s\n", c.GetId())
	fmt.Fprintf(w, "Title:\t%s\n", c.GetTitle())
	fmt.Fprintf(w, "Project:\t%s\n", c.GetProjectId())
	fmt.Fprintf(w, "Workspace:\t%s\n", chatWorkspace(c))
	machine := machineLabel(c.GetActiveDaemonId(), names)
	if c.GetNoMachine() {
		machine = "none (a chat with no machine)"
	}
	fmt.Fprintf(w, "Machine:\t%s\n", machine)
	if wf := c.GetWorkflowName(); wf != "" {
		fmt.Fprintf(w, "Workflow:\t%s\n", wf)
	}
	fmt.Fprintf(w, "State:\t%s\n", chatStateName(c.GetState()))
	fmt.Fprintf(w, "Last active:\t%s\n", c.GetLastActive())
	return w.Flush()
}

// truncateTitle keeps a table row on one line.
func truncateTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 48 {
		return string(r[:47]) + "…"
	}
	return s
}

var chatJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}

func writeProtoJSON(out io.Writer, m proto.Message) error {
	b, err := chatJSON.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

// writeChatsJSON prints {"chats": [...]}, each chat in the proto's JSON
// form.
func writeChatsJSON(out io.Writer, chats []*reliantv1.Chat) error {
	items := make([]json.RawMessage, 0, len(chats))
	for _, c := range chats {
		b, err := chatJSON.Marshal(c)
		if err != nil {
			return err
		}
		items = append(items, b)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string][]json.RawMessage{"chats": items})
}
