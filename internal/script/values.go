package script

import (
	"time"

	"github.com/Shopify/go-lua"

	"btcx/internal/model"
)

// This file converts model values into the plain Lua tables handed to hooks.
// Every table is rebuilt per state, because a state cannot be shared.

// pushEnvelope pushes the envelope table and returns the number of values
// pushed (1).
func pushEnvelope(l *lua.State, env model.Envelope) int {
	l.CreateTable(0, 11)
	pushString(l, "message_id", env.MessageID)
	pushString(l, "subject", env.Subject)
	pushString(l, "date", env.Date.Format(time.RFC3339))
	pushString(l, "mailbox", env.Ref.Mailbox)
	pushNumber(l, "uid", float64(env.Ref.UID))
	pushNumber(l, "uid_validity", float64(env.Ref.UIDValidity))
	pushNumber(l, "size", float64(env.Size))
	pushAddresses(l, "from", env.From)
	pushAddresses(l, "to", env.To)
	pushAddresses(l, "cc", env.Cc)
	l.CreateTable(0, 6)
	pushBoolean(l, "seen", env.Flags.Seen)
	pushBoolean(l, "answered", env.Flags.Answered)
	pushBoolean(l, "flagged", env.Flags.Flagged)
	pushBoolean(l, "deleted", env.Flags.Deleted)
	pushBoolean(l, "draft", env.Flags.Draft)
	pushBoolean(l, "recent", env.Flags.Recent)
	l.SetField(-2, "flags")
	pushBoolean(l, "has_attachments", env.HasAttachments)
	return 1
}

// pushMessage pushes the message table and returns the number of values
// pushed (1). Attachment payloads are summarised by size, never copied.
func pushMessage(l *lua.State, m *model.Message) int {
	l.CreateTable(0, 7)
	pushString(l, "subject", m.Header.Get("Subject"))
	pushAddresses(l, "from", headerAddresses(m.Header, "From"))
	pushAddresses(l, "to", headerAddresses(m.Header, "To"))
	pushAddresses(l, "cc", headerAddresses(m.Header, "Cc"))
	pushString(l, "plain", m.Plain)
	pushString(l, "html", m.HTML)
	l.CreateTable(len(m.Attachments), 0)
	for i, a := range m.Attachments {
		l.CreateTable(0, 5)
		pushString(l, "filename", a.Filename)
		pushString(l, "content_type", a.ContentType)
		pushString(l, "content_id", a.ContentID)
		pushNumber(l, "size", float64(len(a.Data)))
		pushBoolean(l, "inline", a.Inline)
		l.RawSetInt(-2, i+1)
	}
	l.SetField(-2, "attachments")
	return 1
}

// headerAddresses parses an address header for a hook. Unparsable headers
// yield an empty list: the fields are informational, not authority.
func headerAddresses(h model.Header, key string) []model.Address {
	list, _ := model.ParseAddressList(h.Get(key))
	return list
}

// pushAddresses pushes and assigns an array of {name=, addr=} tables.
func pushAddresses(l *lua.State, key string, list []model.Address) {
	l.CreateTable(len(list), 0)
	for i, a := range list {
		l.CreateTable(0, 2)
		pushString(l, "name", a.Name)
		pushString(l, "addr", a.Addr)
		l.RawSetInt(-2, i+1)
	}
	l.SetField(-2, key)
}

// The helpers below assign a field on the table at index -2.

func pushString(l *lua.State, key, value string) {
	l.PushString(value)
	l.SetField(-2, key)
}

func pushNumber(l *lua.State, key string, value float64) {
	l.PushNumber(value)
	l.SetField(-2, key)
}

func pushBoolean(l *lua.State, key string, value bool) {
	l.PushBoolean(value)
	l.SetField(-2, key)
}
