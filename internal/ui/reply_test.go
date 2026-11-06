package ui

import (
	"testing"

	"btcx/internal/contract"
)

func TestReplyActions(t *testing.T) {
	tests := []struct {
		name     string
		listPost string
		want     []replyAction
	}{
		{
			name:     "no list post",
			listPost: "",
			want: []replyAction{
				{"Reply", contract.ReplyDirect},
				{"Reply to All", contract.ReplyAll},
			},
		},
		{
			name:     "list post",
			listPost: "list@example.org",
			want: []replyAction{
				{"Reply", contract.ReplyDirect},
				{"Reply to All", contract.ReplyAll},
				{"Reply to List", contract.ReplyList},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replyActions(tt.listPost)
			if len(got) != len(tt.want) {
				t.Fatalf("replyActions(%q) returned %d items, want %d: %v", tt.listPost, len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if tt.listPost == "" {
				for _, a := range got {
					if a.mode == contract.ReplyList {
						t.Errorf("replyActions(%q) has a list entry without List-Post", tt.listPost)
					}
				}
			}
		})
	}
}
