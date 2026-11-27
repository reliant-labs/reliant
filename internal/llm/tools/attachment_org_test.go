package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// Every tool that makes an attachment records the org of the chat it ran in,
// and none for a chat in no org: an org-scoped read
// (store.GetAttachmentForUserInOrg) takes only an attachment made in its org.
func TestToolAttachmentsRecordTheChatsOrg(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		chat *db.Chat
		want *string
	}{
		{name: "a chat in an org", chat: &db.Chat{UserID: "test-user", OrgID: ptr.Of("org-1")}, want: ptr.Of("org-1")},
		{name: "a chat in no org", chat: &db.Chat{UserID: "test-user"}, want: nil},
		{name: "a blank org", chat: &db.Chat{UserID: "test-user", OrgID: ptr.Of("")}, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("generate_image", func(t *testing.T) {
				repo := newFakeAttachmentRepo()
				repo.chat = tt.chat
				tool := &generateImageTool{repo: repo, resolve: resolverFor(okGenerator())}
				resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "a red bicycle"})
				require.NoError(t, err)
				require.False(t, resp.IsError, resp.Content)
				var output GenerateImageOutput
				require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
				require.NotNil(t, repo.stored[output.AttachmentID])
				assert.Equal(t, tt.want, repo.stored[output.AttachmentID].OrgID)
			})

			t.Run("generate_video", func(t *testing.T) {
				repo := newFakeAttachmentRepo()
				repo.chat = tt.chat
				tool := newVideoTool(repo, newFakeVideoJobs(), &fakeVideoGenerator{caps: veoCaps()})
				resp, err := tool.Execute(videoCtx(t, "tc-org"), GenerateVideoParams{Prompt: "x"})
				require.NoError(t, err)
				require.False(t, resp.IsError, resp.Content)
				var output GenerateVideoOutput
				require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
				require.NotNil(t, repo.stored[output.AttachmentID])
				assert.Equal(t, tt.want, repo.stored[output.AttachmentID].OrgID)
			})

			t.Run("present_image", func(t *testing.T) {
				repo := newFakeAttachmentRepo()
				tool := &presentImageTool{repo: &presentImageTestRepo{fakeAttachmentRepo: repo, chat: tt.chat}}
				ctx := saveAttachmentCtx(t)
				ctx.ChatID = "active-chat"
				ctx = ctx.WithDaemon(&imageFileDaemon{files: map[string][]byte{"/tmp/shot.png": pngOf(t, 4, 4)}})
				resp, err := tool.Execute(ctx, PresentImageParams{Paths: []string{"/tmp/shot.png"}})
				require.NoError(t, err)
				require.False(t, resp.IsError, resp.Content)
				var output PresentImageOutput
				require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &output))
				require.Len(t, output.AttachmentIDs, 1)
				assert.Equal(t, tt.want, repo.stored[output.AttachmentIDs[0]].OrgID)
			})
		})
	}
}
